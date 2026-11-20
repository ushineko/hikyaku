package service

import (
	"context"
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

// validName is what a listener may be called: short, and safe to print in a
// Slack message as it stands.
var validName = regexp.MustCompile(`^[a-z0-9._-]{1,40}$`)

// listener is one agent waiting for an instruction.
type listener struct {
	name string
	from string
	got  chan delivery // buffered, one instruction
}

type delivery struct {
	handle string
	msg    store.Reply
}

// Answers the bot posts in an instruction's thread.
const (
	answerNobody = "No agent is listening, so this was not delivered."
	answerTaken  = "Picked up."
)

func answerSeveral(names []string) string {
	return fmt.Sprintf("Several agents are listening: %s. Start the message with one of their names and a colon, "+
		"as in \"%s: …\". This one was not delivered.", strings.Join(names, ", "), names[0])
}

// listen waits for an instruction addressed to this listener (R1, R4).
func (s *Service) listen(ctx context.Context, req ipc.Request) ipc.Response {
	name := strings.ToLower(req.Name)
	if !validName.MatchString(name) {
		return ipc.Fail(ipc.CodeBadRequest, "a listener's name is 1 to 40 letters, digits, '.', '_' or '-'")
	}
	l := &listener{name: name, from: req.From, got: make(chan delivery, 1)}
	s.mu.Lock()
	if _, taken := s.listeners[name]; taken {
		s.mu.Unlock()
		return ipc.Fail(ipc.CodeBadRequest, "a listener named "+name+" is already waiting; choose another --name")
	}
	s.listeners[name] = l
	s.mu.Unlock()
	defer s.dropListener(l)

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultListenTimeout
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case d := <-l.got:
		return ipc.Response{OK: true, Handle: d.handle, Replies: []store.Reply{d.msg}}
	case <-t.C:
		return ipc.Response{Code: ipc.CodeTimeout, Error: "no instruction before the timeout"}
	case <-ctx.Done():
		s.dropListener(l)
		select {
		case d := <-l.got:
			s.log.Warn("a listener went away as it was given an instruction", "listener", name, "handle", d.handle)
		default:
		}
		return ipc.Response{Code: ipc.CodeTimeout, Error: "the client went away"}
	}
}

func (s *Service) dropListener(l *listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listeners[l.name] == l {
		delete(s.listeners, l.name)
	}
}

// listenerNames are the waiting listeners' names, sorted.
func (s *Service) listenerNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.namesLocked()
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
// is addressed to, and answers in its thread either way (R4 to R6). An
// instruction nobody takes is not kept.
func (s *Service) instruction(ctx context.Context, m slackio.Incoming) {
	if !slices.Contains(s.repliers, m.User) {
		s.log.Warn("ignored an instruction from a user who is not allowed", "user", m.User)
		return
	}
	// The hand-over happens under the lock, so a listener whose client goes
	// away either was never chosen or finds the instruction in its channel.
	s.mu.Lock()
	l, text := s.routeLocked(m.Text)
	names := s.namesLocked()
	var h string
	if l != nil {
		delete(s.listeners, l.name)
		var err error
		if h, err = s.store.Add(m.Channel, m.TS, ""); err != nil {
			s.log.Error("record instruction", "err", err)
		}
		l.got <- delivery{handle: h, msg: store.Reply{User: m.User, Text: text, TS: m.TS, At: s.now().UTC()}}
	}
	s.mu.Unlock()

	if l == nil {
		answer := answerNobody
		if len(names) > 1 {
			answer = answerSeveral(names)
		}
		s.log.Info("no listener took an instruction", "listening", len(names))
		go s.answer(ctx, m, slackio.Outgoing{Text: answer})
		return
	}
	from := l.from
	if from == "" {
		from = l.name
	}
	s.log.Info("a listener took an instruction", "listener", l.name, "handle", h)
	go s.answer(ctx, m, slackio.Outgoing{Text: answerTaken, From: from})
}

// routeLocked picks the listener an instruction is for and the text it
// gets. A word before a colon is an address only when it is a waiting
// listener's name; otherwise the instruction goes to the only listener, or
// to none when there are several. s.mu is held.
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

// answer posts in an instruction's thread.
func (s *Service) answer(ctx context.Context, m slackio.Incoming, msg slackio.Outgoing) {
	msg.ThreadTS = m.TS
	if _, _, err := s.poster.Post(ctx, m.Channel, msg); err != nil {
		s.log.Warn("answer an instruction", "err", s.redact.String(err.Error()))
	}
}
