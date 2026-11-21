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

	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/buildinfo"
	"github.com/ushineko/hikyaku/internal/config"
	"github.com/ushineko/hikyaku/internal/ipc"
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

	mu        sync.Mutex
	waiters   map[string][]chan store.Reply
	listeners map[string]*listener
	dm        string

	// desk is the answerer (spec 007); nil when the service has none.
	desk *desk
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
	}
	if d.Runner != nil {
		desk, err := newDesk(d.Runner, d.Settings.Answerer, d.AnswererState)
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
		s.log.Warn("ignored a reply from a user who is not allowed", "user", m.User, "handle", handle)
		return
	}
	r := store.Reply{User: m.User, Text: m.Text, TS: m.TS, At: s.now().UTC()}
	if err := s.store.AddReply(handle, r); err != nil {
		s.log.Error("record reply", "handle", handle, "err", err)
	}
	s.mu.Lock()
	ws := s.waiters[handle]
	delete(s.waiters, handle)
	s.mu.Unlock()
	for _, w := range ws {
		w <- r // buffered, one per waiter
	}
	if len(ws) == 0 {
		s.offerFollowUp(ctx, m.Channel, m.ThreadTS, m.User, m.Text)
	}
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
		return ipc.Response{OK: true, Status: &ipc.Status{
			PID: os.Getpid(), Version: buildinfo.Version(), Connected: s.connected.Load(),
			Owner: s.settings.Owner, Repliers: len(s.repliers), Listeners: s.listenerNames(),
			Answerer: s.answerStatus(),
		}}
	case ipc.OpAnswerer:
		return s.answerSwitch(ctx, req)
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
	channel, thread, resp := s.where(ctx, req)
	if channel == "" {
		return "", resp
	}
	posted, ts, err := s.poster.Post(ctx, channel, slackio.Outgoing{
		Text: req.Text, Title: req.Title, Status: status, From: req.From, Question: req.Question,
		ThreadTS: thread,
	})
	if err != nil {
		return "", s.slackFail(err)
	}
	h, err := s.store.Add(posted, ts, thread)
	if err != nil {
		s.log.Error("record message", "err", err)
		return "", ipc.Fail(ipc.CodeInternal, "the message was posted but could not be recorded: "+s.redact.String(err.Error()))
	}
	return h, ipc.Response{}
}

// where is the channel a message goes to and, for --thread, the thread in
// it: the thread of the message with that handle, in that message's channel.
func (s *Service) where(ctx context.Context, req ipc.Request) (channel, thread string, resp ipc.Response) {
	if req.Thread == "" {
		channel, resp = s.destination(ctx, req.Channel)
		return channel, "", resp
	}
	if req.Channel != "" {
		return "", "", ipc.Fail(ipc.CodeBadRequest, "give --thread or --channel, not both: the thread decides the channel")
	}
	m, err := s.store.Get(req.Thread)
	if errors.Is(err, store.ErrUnknownHandle) {
		return "", "", ipc.Fail(ipc.CodeUnknownHandle, "unknown or expired handle "+req.Thread)
	}
	if err != nil {
		return "", "", ipc.Fail(ipc.CodeInternal, s.redact.String(err.Error()))
	}
	return m.Channel, m.Root(), ipc.Response{}
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
		return ipc.Response{Code: ipc.CodeTimeout, Error: "no reply before the timeout", Handle: h}
	case <-ctx.Done():
		return ipc.Response{Code: ipc.CodeTimeout, Error: "the client went away", Handle: h}
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
