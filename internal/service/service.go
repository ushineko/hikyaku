/*
Package service is the running hikyaku: it answers the CLI's requests,
posts to Slack, and matches thread replies to the messages that asked for
them.
*/
package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ushineko/hikyaku/internal/access"
	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/buildinfo"
	"github.com/ushineko/hikyaku/internal/config"
	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/persona"
	"github.com/ushineko/hikyaku/internal/secret"
	"github.com/ushineko/hikyaku/internal/slackio"
	"github.com/ushineko/hikyaku/internal/store"
)

// DefaultWaitTimeout is how long a wait lasts when the request names no timeout.
const DefaultWaitTimeout = time.Hour

// Service is the running hikyaku.
type Service struct {
	poster   slackio.Poster
	listener slackio.Listener
	store    *store.Store
	settings config.Settings
	repliers []string
	log      *slog.Logger
	redact   *secret.Redactor
	now      func() time.Time

	connected atomic.Bool
	// noReactions is set once Slack has said the app lacks reactions:write.
	noReactions atomic.Bool
	// bg is the context for work that outlives a request: hold timers and
	// the notes they post. Run replaces it with its own.
	bg context.Context

	mu        sync.Mutex
	waiters   map[string][]chan store.Reply
	listeners map[string]*listener
	dm        string
	// instrs are the instructions handed to listeners, by handle (spec 006).
	instrs map[string]*instr
	// owners is the listener an instruction, or a message posted top-level
	// with --reply-to on one, belongs to, by handle.
	owners map[string]string
	// convo is, per listener, the handle of a question asked top-level
	// with --reply-to whose answer is the listener's next message (R6).
	convo map[string]string

	// desk is the answerer (spec 007); nil when the service has none.
	desk *desk
	// persona is the bot's persona (spec 008).
	persona *persona.Loader
	// admins and access are the roles (spec 009); self is the bot's own
	// user ID, learnt when Run starts.
	admins []string
	access *access.List
	self   string
	paths  answerer.Paths
}

// Deps is what a Service is built from.
type Deps struct {
	Poster   slackio.Poster
	Listener slackio.Listener
	Store    *store.Store
	Settings config.Settings
	Log      *slog.Logger
	Redact   *secret.Redactor
	Now      func() time.Time
	// Runner runs the answerer's questions; nil means no answerer.
	Runner answerer.Runner
	// AnswererState is the file the answerer's switch and spending are
	// kept in.
	AnswererState string
	// Persona reads the persona file; nil is the built-in default.
	Persona *persona.Loader
	// PlainRunner runs questions from people other than an admin, with no
	// tools (spec 009); nil uses Runner.
	PlainRunner answerer.Runner
	// Access is the users list (spec 009); nil means no users.
	Access *access.List
	// Paths are the folders whose absolute paths answers must not show.
	Paths answerer.Paths
}

// New builds a Service.
func New(d Deps) (*Service, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Redact == nil {
		d.Redact = &secret.Redactor{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		poster: d.Poster, listener: d.Listener, store: d.Store, settings: d.Settings,
		repliers: d.Settings.Repliers(), log: d.Log, redact: d.Redact, now: d.Now,
		waiters: map[string][]chan store.Reply{}, listeners: map[string]*listener{},
		instrs: map[string]*instr{}, owners: map[string]string{}, convo: map[string]string{},
		bg: context.Background(), persona: d.Persona,
		admins: d.Settings.AdminIDs(), access: d.Access, paths: d.Paths,
	}
	if d.Runner != nil {
		desk, err := newDesk(d.Runner, d.PlainRunner, d.Settings.Answerer, d.AnswererState)
		if err != nil {
			return nil, err
		}
		s.desk = desk
	}
	return s, nil
}

// Run listens to Slack until ctx is cancelled. It returns an error only when
// the listener gives up for good, which is when Slack refuses the tokens.
func (s *Service) Run(ctx context.Context) error {
	if len(s.repliers) == 0 {
		s.log.Warn("no owner and no allowed_repliers in the settings: no reply will reach an agent")
	}
	self, err := s.poster.Self(ctx)
	if err != nil {
		s.log.Warn("learn the bot's own user ID; mentions in channels will not be heard",
			"err", s.redact.String(err.Error()))
	}
	s.mu.Lock()
	s.bg, s.self = ctx, self
	s.mu.Unlock()
	in := make(chan slackio.Incoming, 16)
	errc := make(chan error, 1)
	go func() { errc <- s.listener.Listen(ctx, in, s.connected.Store) }()
	if s.desk != nil {
		go s.answerLoop(ctx)
	}
	for {
		select {
		case err := <-errc:
			return err
		case m := <-in:
			switch {
			case m.Joined:
				s.joined(ctx, m)
			case !m.InDM() && (s.mentionsMe(m.Text) || s.askerInOwnThread(m)):
				s.channelMessage(ctx, m)
			case m.ThreadTS != "":
				s.reply(ctx, m)
			case m.InDM():
				s.instruction(ctx, m)
			}
		}
	}
}

// reply records a thread reply from an allowed user and wakes whoever is
// asking. A reply from anyone else, in one of our threads, is dropped and
// logged by user ID only: its text is not ours to keep.
func (s *Service) reply(ctx context.Context, m slackio.Incoming) {
	handle, ours := s.store.Find(m.Channel, m.ThreadTS, m.TS)
	if !ours {
		return
	}
	if !slices.Contains(s.repliers, m.User) {
		// A user with the DM grant, who may not answer agents, can still
		// follow up on an answer in their DM (spec 009, R2).
		if m.InDM() && s.mayDM(m.User) {
			s.offerFollowUp(ctx, m.Channel, m.ThreadTS, m.User, m.Text)
			return
		}
		s.log.Warn("ignored a reply from a user who is not allowed", "user", m.User, "handle", handle)
		return
	}
	r := store.Reply{User: m.User, Text: m.Text, TS: m.TS, At: s.now().UTC()}
	if !s.deliverReply(handle, r) && m.InDM() {
		s.offerFollowUp(ctx, m.Channel, m.ThreadTS, m.User, m.Text)
	}
}

// deliverReply records a reply to the message with the handle and wakes
// whoever waits on it. It reports whether anyone did. A conversation whose
// question it answers is over (spec 006, R6).
func (s *Service) deliverReply(handle string, r store.Reply) bool {
	if err := s.store.AddReply(handle, r); err != nil {
		s.log.Error("record reply", "handle", handle, "err", err)
	}
	s.mu.Lock()
	ws := s.waiters[handle]
	delete(s.waiters, handle)
	for name, q := range s.convo {
		if q == handle {
			delete(s.convo, name)
		}
	}
	s.mu.Unlock()
	for _, w := range ws {
		w <- r // buffered, one per waiter
	}
	return len(ws) > 0
}

// Handle answers one CLI request.
func (s *Service) Handle(ctx context.Context, req ipc.Request) ipc.Response {
	switch req.Op {
	case ipc.OpSend:
		h, resp := s.send(ctx, req)
		if h == "" {
			return resp
		}
		return ipc.Response{OK: true, Handle: h}
	case ipc.OpWait:
		return s.wait(ctx, req)
	case ipc.OpListen:
		return s.listen(ctx, req)
	case ipc.OpInbox:
		return s.inbox(req)
	case ipc.OpStatus:
		names, states := s.listenerStates()
		return ipc.Response{OK: true, Status: &ipc.Status{
			PID: os.Getpid(), Version: buildinfo.Version(), Connected: s.connected.Load(),
			Owner: s.settings.Owner, Repliers: len(s.repliers), Listeners: names, ListenerStates: states,
			Answerer: s.answerStatus(),
		}}
	case ipc.OpAnswerer:
		return s.answerSwitch(ctx, req)
	case ipc.OpUsers:
		return s.usersOp(req)
	default:
		return ipc.Fail(ipc.CodeBadRequest, "unknown operation "+req.Op)
	}
}

func (s *Service) send(ctx context.Context, req ipc.Request) (string, ipc.Response) {
	status, err := slackio.ParseStatus(req.Status)
	if err != nil {
		return "", ipc.Fail(ipc.CodeBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Text) == "" && req.Title == "" {
		return "", ipc.Fail(ipc.CodeBadRequest, "nothing to send: give text or a title")
	}
	channel, thread, target, resp := s.where(ctx, req)
	if channel == "" {
		return "", resp
	}
	posted, ts, err := s.poster.Post(ctx, channel, slackio.Outgoing{
		Text: req.Text, Title: req.Title, Status: status, From: req.From,
		Question: req.Question && target == nil,
		ThreadTS: thread,
	})
	if err != nil {
		return "", s.slackFail(err)
	}
	var h string
	if target != nil && target.TopLevel {
		h, err = s.store.AddTopLevel(posted, ts)
	} else {
		h, err = s.store.Add(posted, ts, thread)
	}
	if err != nil {
		s.log.Error("record message", "err", err)
		return "", ipc.Fail(ipc.CodeInternal, "the message was posted but could not be recorded: "+s.redact.String(err.Error()))
	}
	if target != nil {
		s.answeredBy(target, h, req)
	}
	return h, ipc.Response{}
}

// answeredBy notes that the message h answers target (spec 006): ✅ on an
// instruction answered for the first time, and, for a message posted
// top-level beside it, the listener it belongs to and, for a question,
// that the listener's next message is its answer (R6).
func (s *Service) answeredBy(target *store.Message, h string, req ipc.Request) {
	s.answered(target.Handle)
	if !target.TopLevel {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.owners[target.Handle]
	if owner == "" {
		return
	}
	s.owners[h] = owner
	if req.Question {
		s.convo[owner] = h
	}
}

// where is the channel a message goes to and the thread in it, if any. For
// --thread, the thread of the message with that handle, in its channel; for
// --reply-to, beside the message when it is top-level (an instruction, or a
// message posted beside one), in its thread otherwise. target is that
// message.
func (s *Service) where(ctx context.Context, req ipc.Request) (channel, thread string, target *store.Message,
	resp ipc.Response) {
	h := req.Thread
	switch {
	case req.Thread != "" && req.ReplyTo != "":
		return "", "", nil, ipc.Fail(ipc.CodeBadRequest, "give --reply-to or --thread, not both")
	case req.ReplyTo != "":
		h = req.ReplyTo
	case req.Thread == "":
		channel, resp = s.destination(ctx, req.Channel)
		return channel, "", nil, resp
	}
	if req.Channel != "" {
		return "", "", nil, ipc.Fail(ipc.CodeBadRequest,
			"give --thread or --reply-to, or --channel, not both: the message answered decides the channel")
	}
	m, err := s.store.Get(h)
	if errors.Is(err, store.ErrUnknownHandle) {
		return "", "", nil, ipc.Fail(ipc.CodeUnknownHandle, "unknown or expired handle "+h)
	}
	if err != nil {
		return "", "", nil, ipc.Fail(ipc.CodeInternal, s.redact.String(err.Error()))
	}
	if req.ReplyTo != "" && m.TopLevel {
		return m.Channel, "", &m, ipc.Response{}
	}
	if req.ReplyTo == "" {
		m.TopLevel = false // --thread posts in the thread, whatever the message is
	}
	return m.Channel, m.Root(), &m, ipc.Response{}
}

func (s *Service) destination(ctx context.Context, channel string) (string, ipc.Response) {
	if channel != "" {
		return channel, ipc.Response{}
	}
	s.mu.Lock()
	dm := s.dm
	s.mu.Unlock()
	if dm != "" {
		return dm, ipc.Response{}
	}
	if s.settings.Owner == "" {
		return "", ipc.Fail(ipc.CodeNotConfigured,
			"no owner in the settings: set owner to your Slack user ID, or pass --channel")
	}
	dm, err := s.poster.OpenDM(ctx, s.settings.Owner)
	if err != nil {
		return "", s.slackFail(err)
	}
	s.mu.Lock()
	s.dm = dm
	s.mu.Unlock()
	return dm, ipc.Response{}
}

// wait returns the first reply to a message, waiting for it if none has
// arrived. `ask` posts and then waits, so a reply that lands between the two
// is already in the store when the wait begins.
func (s *Service) wait(ctx context.Context, req ipc.Request) ipc.Response {
	h := req.Handle
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	w := make(chan store.Reply, 1)
	s.mu.Lock()
	s.waiters[h] = append(s.waiters[h], w)
	s.mu.Unlock()
	defer s.dropWaiter(h, w)

	m, err := s.store.Get(h)
	if errors.Is(err, store.ErrUnknownHandle) {
		return ipc.Fail(ipc.CodeUnknownHandle, "unknown or expired handle "+h)
	}
	if err != nil {
		return ipc.Fail(ipc.CodeInternal, s.redact.String(err.Error()))
	}
	if len(m.Replies) > 0 {
		return ipc.Response{OK: true, Handle: h, Replies: m.Replies[:1]}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-w:
		return ipc.Response{OK: true, Handle: h, Replies: []store.Reply{r}}
	case <-t.C:
		s.endConversation(h)
		return ipc.Response{Code: ipc.CodeTimeout, Error: "no reply before the timeout", Handle: h}
	case <-ctx.Done():
		s.endConversation(h)
		return ipc.Response{Code: ipc.CodeTimeout, Error: "the client went away", Handle: h}
	}
}

// endConversation stops a question nobody waits on any more from taking
// its listener's next message (R6).
func (s *Service) endConversation(h string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiters[h]) > 1 { // this waiter is still among them
		return
	}
	for name, q := range s.convo {
		if q == h {
			delete(s.convo, name)
		}
	}
}

func (s *Service) dropWaiter(h string, w chan store.Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiters[h] = slices.DeleteFunc(s.waiters[h], func(x chan store.Reply) bool { return x == w })
	if len(s.waiters[h]) == 0 {
		delete(s.waiters, h)
	}
}

func (s *Service) inbox(req ipc.Request) ipc.Response {
	m, err := s.store.Get(req.Handle)
	if errors.Is(err, store.ErrUnknownHandle) {
		return ipc.Fail(ipc.CodeUnknownHandle, "unknown or expired handle "+req.Handle)
	}
	if err != nil {
		return ipc.Fail(ipc.CodeInternal, s.redact.String(err.Error()))
	}
	return ipc.Response{OK: true, Handle: m.Handle, Replies: m.Replies}
}

func (s *Service) slackFail(err error) ipc.Response {
	msg := s.redact.String(err.Error())
	s.log.Warn("slack request failed", "err", msg)
	return ipc.Fail(ipc.CodeSlack, msg)
}
