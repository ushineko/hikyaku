package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/slackio"
	"github.com/ushineko/hikyaku/internal/store"
)

// DefaultListenTimeout is how long a listen lasts when the request names no
// timeout.
const DefaultListenTimeout = time.Hour

// MaxHold is the longest a listener's name is held (spec 006, R1).
const MaxHold = 24 * time.Hour

// maxQueue is how many instructions wait for one held listener (R2).
const maxQueue = 10

// validName is what a listener may be called: short, and safe to print in a
// Slack message as it stands.
var validName = regexp.MustCompile(`^[a-z0-9._-]{1,40}$`)

// answererPrefix sends a message to the answerer whatever is listening
// (R10). It is not a listener's name.
const answererPrefix = "answerer"

// listener is one agent listening: waiting while a listen call is open
// (got is set), held between calls.
type listener struct {
	name string
	from string
	hold time.Duration
	got  chan delivery // buffered, one instruction; nil while held
	// queue is what arrived while held, oldest first.
	queue []delivery
	// gen changes whenever the listener is resumed, so a hold timer that
	// fires late knows it is stale.
	gen   int
	timer *time.Timer
}

type delivery struct {
	handle  string
	channel string
	msg     store.Reply
}

// instr is an instruction the service handed to a listener, for its
// reactions.
type instr struct {
	channel, ts string
	answered    bool
}

// Reactions on a person's message (R5), by Slack's emoji names.
const (
	reactQueued   = "hourglass_flowing_sand"
	reactTaken    = "eyes"
	reactAnswered = "white_check_mark"
	reactDropped  = "mailbox_with_no_mail"
)

// Answers the bot posts in an instruction's thread.
const (
	answerNobody      = "No agent is listening, so this was not delivered."
	answerTaken       = "Picked up."
	answerAnswererOff = "The answerer is off."
)

func answerSeveral(names []string) string {
	return fmt.Sprintf("Several agents are listening: %s. Start the message with one of their names and a colon, "+
		"as in \"%s: …\". This one was not delivered.", strings.Join(names, ", "), names[0])
}

func answerQueued(name string) string {
	return fmt.Sprintf("Queued: *%s* is busy with an earlier instruction.", name)
}

func answerFull(name string) string {
	return fmt.Sprintf("Not delivered: *%s* already has ten instructions waiting.", name)
}

func answerGone(name string) string {
	return fmt.Sprintf("Not delivered: *%s* stopped listening.", name)
}

// noticeGone is a /me, so Slack shows it after the bot's name: "nykbot-α
// stopped listening for claude".
func noticeGone(name string) string { return fmt.Sprintf("stopped listening for *%s*.", name) }

// listen waits for an instruction addressed to this listener (spec 005 R1,
// R4), resuming a held one (R1) and taking what queued for it first (R2).
func (s *Service) listen(ctx context.Context, req ipc.Request) ipc.Response {
	name := strings.ToLower(req.Name)
	switch {
	case !validName.MatchString(name):
		return ipc.Fail(ipc.CodeBadRequest, "a listener's name is 1 to 40 letters, digits, '.', '_' or '-'")
	case name == answererPrefix:
		return ipc.Fail(ipc.CodeBadRequest, "\"answerer\" addresses the answerer; choose another --name")
	case req.Hold < 0 || req.Hold > MaxHold:
		return ipc.Fail(ipc.CodeBadRequest, "--hold is from 0 to 24h")
	}
	s.mu.Lock()
	l := s.listeners[name]
	if l != nil && l.got != nil {
		s.mu.Unlock()
		return ipc.Fail(ipc.CodeBadRequest, "a listener named "+name+" is already waiting; choose another --name")
	}
	if l == nil {
		l = &listener{name: name}
		s.listeners[name] = l
	}
	l.from, l.hold = req.From, req.Hold
	l.gen++
	if l.timer != nil {
		l.timer.Stop()
	}
	if len(l.queue) > 0 {
		d := l.queue[0]
		l.queue = l.queue[1:]
		gone := s.endLocked(l)
		s.mu.Unlock()
		s.taken(d, l)
		s.whenGone(gone)
		return ipc.Response{OK: true, Handle: d.handle, Replies: []store.Reply{d.msg}}
	}
	got := make(chan delivery, 1)
	l.got = got
	s.mu.Unlock()

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultListenTimeout
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case d := <-got:
		return ipc.Response{OK: true, Handle: d.handle, Replies: []store.Reply{d.msg}}
	case <-t.C:
		if d, ok := s.stopWaiting(l, got, false); ok {
			return ipc.Response{OK: true, Handle: d.handle, Replies: []store.Reply{d.msg}}
		}
		return ipc.Response{Code: ipc.CodeTimeout, Error: "no instruction before the timeout"}
	case <-ctx.Done():
		s.stopWaiting(l, got, true)
		return ipc.Response{Code: ipc.CodeTimeout, Error: "the client went away"}
	}
}

// stopWaiting ends a listen call that timed out or lost its client. An
// instruction handed over at the same moment is returned when the client
// is still there to take it, and put back in the queue when it is not.
func (s *Service) stopWaiting(l *listener, got chan delivery, clientGone bool) (delivery, bool) {
	s.mu.Lock()
	var d delivery
	handed := l.got != got
	if handed {
		d = <-got
		if !clientGone {
			s.mu.Unlock()
			return d, true
		}
		l.queue = append([]delivery{d}, l.queue...)
		s.log.Warn("a listener went away as it was given an instruction; it is queued again",
			"listener", l.name, "handle", d.handle)
	} else {
		l.got = nil
	}
	gone := s.endLocked(l)
	s.mu.Unlock()
	if handed {
		s.reactions(d.channel, d.msg.TS, reactTaken, reactQueued, slackio.Outgoing{})
	}
	if gone != nil && !handed {
		gone.notice = true
	}
	s.whenGone(gone)
	return delivery{}, false
}

// departure is a listener that has gone: what was queued for it, and
// whether to say so in the DM.
type departure struct {
	name   string
	queue  []delivery
	notice bool
}

// endLocked starts the hold of a listener whose listen call has ended. With
// no hold, the listener goes at once and its departure is returned, for
// whenGone after the lock is released. s.mu is held.
func (s *Service) endLocked(l *listener) *departure {
	if l.hold <= 0 {
		delete(s.listeners, l.name)
		delete(s.convo, l.name)
		return &departure{name: l.name, queue: l.queue, notice: len(l.queue) > 0}
	}
	gen := l.gen
	l.timer = time.AfterFunc(l.hold, func() { s.expire(l.name, gen) })
	return nil
}

// expire ends a hold nobody resumed (R3).
func (s *Service) expire(name string, gen int) {
	s.mu.Lock()
	l := s.listeners[name]
	if l == nil || l.gen != gen || l.got != nil {
		s.mu.Unlock()
		return
	}
	delete(s.listeners, name)
	delete(s.convo, name)
	s.mu.Unlock()
	s.log.Info("a listener's hold ended", "listener", name, "queued", len(l.queue))
	s.whenGone(&departure{name: name, queue: l.queue, notice: true})
}

// whenGone answers what was queued for a listener that has gone, and posts
// that it stopped listening.
func (s *Service) whenGone(d *departure) {
	if d == nil {
		return
	}
	ctx := s.background()
	for _, q := range d.queue {
		s.reactions(q.channel, q.msg.TS, reactDropped, reactQueued, slackio.Outgoing{})
		s.post(ctx, q.channel, slackio.Outgoing{Text: answerGone(d.name), ThreadTS: q.msg.TS})
	}
	if !d.notice {
		return
	}
	dm, resp := s.destination(ctx, "")
	if dm == "" {
		s.log.Warn("cannot say a listener stopped", "err", resp.Error)
		return
	}
	if err := s.poster.Me(ctx, dm, noticeGone(d.name)); err != nil {
		s.log.Warn("post /me", "err", s.redact.String(err.Error()))
		s.post(ctx, dm, slackio.Outgoing{Text: "_" + noticeGone(d.name) + "_"})
	}
}

// listenerStates is each listener's state, sorted by name (R7).
func (s *Service) listenerStates() ([]string, []ipc.ListenerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.namesLocked()
	states := make([]ipc.ListenerState, 0, len(names))
	for _, n := range names {
		l := s.listeners[n]
		st := ipc.ListenerState{Name: n, State: ipc.ListenerBusy, Queued: len(l.queue)}
		switch {
		case l.got != nil:
			st.State = ipc.ListenerWaiting
		case s.convo[n] != "":
			st.State = ipc.ListenerConversation
		}
		states = append(states, st)
	}
	return names, states
}

func (s *Service) namesLocked() []string {
	names := make([]string, 0, len(s.listeners))
	for n := range s.listeners {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// instruction hands a top-level DM from an allowed user to the listener it
// is addressed to: a conversation's waiting question (R6), a waiting
// listener, or a held one's queue (R2). `answerer:` goes to the answerer
// (R10). With no listener, the answerer takes it or the bot says why not.
func (s *Service) instruction(ctx context.Context, m slackio.Incoming) {
	if !slices.Contains(s.repliers, m.User) {
		s.log.Warn("ignored an instruction from a user who is not allowed", "user", m.User)
		return
	}
	if rest, ok := addressedTo(answererPrefix, m.Text); ok && m.User == s.settings.Owner {
		if !s.offerQuestion(ctx, m.Channel, m.TS, m.User, rest) {
			go s.answer(ctx, m, slackio.Outgoing{Text: answerAnswererOff})
		}
		return
	}
	r := store.Reply{User: m.User, TS: m.TS, At: s.now().UTC()}
	// The hand-over happens under the lock, so a listener whose client goes
	// away either was never chosen or finds the instruction in its channel.
	s.mu.Lock()
	l, text := s.routeLocked(m.Text)
	names := s.namesLocked()
	r.Text = text
	if l != nil {
		if q := s.convo[l.name]; q != "" {
			delete(s.convo, l.name)
			s.mu.Unlock()
			s.log.Info("a message answered a conversation's question", "listener", l.name, "handle", q)
			s.deliverReply(q, r)
			return
		}
	}
	var (
		h      string
		queued bool
		full   bool
	)
	if l != nil {
		var err error
		if h, err = s.store.AddTopLevel(m.Channel, m.TS); err != nil {
			s.log.Error("record instruction", "err", err)
		}
		s.instrs[h] = &instr{channel: m.Channel, ts: m.TS}
		s.owners[h] = l.name
		d := delivery{handle: h, channel: m.Channel, msg: r}
		switch {
		case l.got != nil:
			l.got <- d
			l.got = nil
			s.endLocked(l) // hold > 0 here or the listener ends silently: it took an instruction
		case len(l.queue) >= maxQueue:
			full = true
		default:
			l.queue = append(l.queue, d)
			queued = true
		}
	}
	s.mu.Unlock()

	switch {
	case l == nil && len(names) == 0 && s.offerQuestion(ctx, m.Channel, m.TS, m.User, m.Text):
	case l == nil:
		answer := answerNobody
		if len(names) > 1 {
			answer = answerSeveral(names)
		}
		s.log.Info("no listener took an instruction", "listening", len(names))
		go s.answer(ctx, m, slackio.Outgoing{Text: answer})
	case full:
		s.log.Info("a listener's queue is full", "listener", l.name)
		go s.answer(ctx, m, slackio.Outgoing{Text: answerFull(l.name)})
	case queued:
		s.log.Info("queued an instruction for a busy listener", "listener", l.name, "handle", h)
		go s.reactions(m.Channel, m.TS, reactQueued, "", slackio.Outgoing{Text: answerQueued(l.name)})
	default:
		s.log.Info("a listener took an instruction", "listener", l.name, "handle", h)
		go s.reactions(m.Channel, m.TS, reactTaken, "", slackio.Outgoing{Text: answerTaken, From: l.fromLine()})
	}
}

func (l *listener) fromLine() string {
	if l.from != "" {
		return l.from
	}
	return l.name
}

// taken marks a queued instruction as taken by its listener's next listen.
func (s *Service) taken(d delivery, l *listener) {
	s.log.Info("a listener took a queued instruction", "listener", l.name, "handle", d.handle)
	go s.reactions(d.channel, d.msg.TS, reactTaken, reactQueued,
		slackio.Outgoing{Text: answerTaken, From: l.fromLine()})
}

// addressedTo reports whether text starts with name and a colon, and
// returns the rest.
func addressedTo(name, text string) (string, bool) {
	word, rest, ok := strings.Cut(text, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(word), name) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// routeLocked picks the listener an instruction is for and the text it
// gets. A word before a colon is an address only when it is a listener's
// name; otherwise the instruction goes to the only listener, or to none
// when there are several. Held listeners count (R1). s.mu is held.
func (s *Service) routeLocked(text string) (*listener, string) {
	if word, rest, ok := strings.Cut(text, ":"); ok {
		if l := s.listeners[strings.ToLower(strings.TrimSpace(word))]; l != nil {
			return l, strings.TrimSpace(rest)
		}
	}
	if len(s.listeners) == 1 {
		for _, l := range s.listeners {
			return l, text
		}
	}
	return nil, ""
}

// reactions puts add on the message at ts and takes remove off it (R5).
// Without the reactions scope, it posts fallback in the message's thread
// instead, when there is one, and says once in the log what is missing.
func (s *Service) reactions(channel, ts, add, remove string, fallback slackio.Outgoing) {
	ctx := s.background()
	if remove != "" {
		if err := s.poster.Unreact(ctx, channel, ts, remove); err != nil && !errors.Is(err, slackio.ErrMissingScope) {
			s.log.Warn("remove a reaction", "err", s.redact.String(err.Error()))
		}
	}
	err := s.poster.React(ctx, channel, ts, add)
	switch {
	case err == nil:
	case errors.Is(err, slackio.ErrMissingScope):
		if s.noReactions.CompareAndSwap(false, true) {
			s.log.Warn("the Slack app has no reactions:write scope; notes are posted instead. " +
				"Add the scope and reinstall the app to see reactions")
		}
		if fallback.Text != "" {
			fallback.ThreadTS = ts
			s.post(ctx, channel, fallback)
		}
	default:
		s.log.Warn("add a reaction", "err", s.redact.String(err.Error()))
	}
}

// answered puts ✅ on an instruction the first time its agent answers it.
func (s *Service) answered(handle string) {
	s.mu.Lock()
	in := s.instrs[handle]
	first := in != nil && !in.answered
	if first {
		in.answered = true
	}
	s.mu.Unlock()
	if first {
		go s.reactions(in.channel, in.ts, reactAnswered, reactTaken, slackio.Outgoing{})
	}
}

// answer posts in an instruction's thread.
func (s *Service) answer(ctx context.Context, m slackio.Incoming, msg slackio.Outgoing) {
	msg.ThreadTS = m.TS
	s.post(ctx, m.Channel, msg)
}

func (s *Service) post(ctx context.Context, channel string, msg slackio.Outgoing) {
	if _, _, err := s.poster.Post(ctx, channel, msg); err != nil {
		s.log.Warn("post a note", "err", s.redact.String(err.Error()))
	}
}

// background is the context for work that outlives a request.
func (s *Service) background() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bg
}
