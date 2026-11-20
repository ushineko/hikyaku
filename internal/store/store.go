/*
Package store remembers the messages hikyaku has sent and the replies they
received, so a handle printed by `send` still means something after the
service restarts.

It is a JSON file written whole on every change. Handles expire, so it stays
small; it is not a database.
*/
package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ushineko/hikyaku/internal/atomicfile"
)

// Reply is one reply from an allowed user in a message's thread.
type Reply struct {
	User string    `json:"user"`
	Text string    `json:"text"`
	TS   string    `json:"ts"`
	At   time.Time `json:"at"`
}

// Message is one message hikyaku posted.
type Message struct {
	Handle  string `json:"handle"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	// Thread is the ts of the thread's first message when this one was
	// posted inside a thread, and empty when it started its own.
	Thread  string    `json:"thread,omitempty"`
	Sent    time.Time `json:"sent"`
	Replies []Reply   `json:"replies,omitempty"`
}

// Root is the ts of the thread the message belongs to.
func (m Message) Root() string {
	if m.Thread != "" {
		return m.Thread
	}
	return m.TS
}

// ErrUnknownHandle is returned for a handle the store does not hold, either
// never issued or expired.
var ErrUnknownHandle = errors.New("unknown or expired handle")

// Store is the message store. It is safe for concurrent use.
type Store struct {
	path      string
	retention time.Duration
	now       func() time.Time

	mu   sync.Mutex
	msgs map[string]*Message
}

// Open loads the store at path, dropping what is older than retention. A
// missing file is an empty store.
func Open(path string, retention time.Duration, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	s := &Store{path: path, retention: retention, now: now, msgs: map[string]*Message{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read message store: %w", err)
	}
	var list []*Message
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse message store %s: %w", path, err)
	}
	for _, m := range list {
		if m != nil && m.Handle != "" {
			s.msgs[m.Handle] = m
		}
	}
	s.prune()
	return s, nil
}

// Add records a posted message and returns its new handle. thread is the
// ts of the thread it was posted in, or empty.
func (s *Store) Add(channel, ts, thread string) (string, error) {
	h, err := newHandle()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs[h] = &Message{Handle: h, Channel: channel, TS: ts, Thread: thread, Sent: s.now()}
	s.prune()
	return h, s.save()
}

// Get returns a copy of the message with the handle.
func (s *Store) Get(handle string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[handle]
	if !ok || s.expired(m) {
		return Message{}, ErrUnknownHandle
	}
	c := *m
	c.Replies = slices.Clone(m.Replies)
	return c, nil
}

// AddReply records a reply to the message with the handle. A reply already
// recorded (by its ts) is not recorded twice.
func (s *Store) AddReply(handle string, r Reply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[handle]
	if !ok || s.expired(m) {
		return ErrUnknownHandle
	}
	if slices.ContainsFunc(m.Replies, func(x Reply) bool { return x.TS == r.TS }) {
		return nil
	}
	m.Replies = append(m.Replies, r)
	return s.save()
}

// Find returns the handle of the message a reply in the thread
// channel/threadTS answers: of our messages in that thread, the latest one
// posted before the reply (replyTS). ok is false when the thread is not ours.
func (s *Store) Find(channel, threadTS, replyTS string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Message
	for _, m := range s.msgs {
		if m.Channel != channel || m.Root() != threadTS || s.expired(m) || !tsBefore(m.TS, replyTS) {
			continue
		}
		if best == nil || tsBefore(best.TS, m.TS) {
			best = m
		}
	}
	if best == nil {
		return "", false
	}
	return best.Handle, true
}

// tsBefore orders two Slack timestamps ("seconds.micros"). A timestamp it
// cannot read sorts by its text.
func tsBefore(a, b string) bool {
	as, af, aok := splitTS(a)
	bs, bf, bok := splitTS(b)
	if !aok || !bok {
		return a < b
	}
	return as < bs || (as == bs && af < bf)
}

func splitTS(ts string) (sec, frac int64, ok bool) {
	whole, part, _ := strings.Cut(ts, ".")
	if len(part) < 6 { // Slack writes six digits; read ".5" as ".500000"
		part += strings.Repeat("0", 6-len(part))
	}
	sec, err1 := strconv.ParseInt(whole, 10, 64)
	frac, err2 := strconv.ParseInt(part, 10, 64)
	return sec, frac, err1 == nil && err2 == nil
}

func (s *Store) expired(m *Message) bool {
	return s.now().Sub(m.Sent) > s.retention
}

func (s *Store) prune() {
	for h, m := range s.msgs {
		if s.expired(m) {
			delete(s.msgs, h)
		}
	}
}

// save writes the store whole.
func (s *Store) save() error {
	list := make([]*Message, 0, len(s.msgs))
	for _, m := range s.msgs {
		list = append(list, m)
	}
	slices.SortFunc(list, func(a, b *Message) int { return a.Sent.Compare(b.Sent) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encode message store: %w", err)
	}
	return atomicfile.Write(s.path, b)
}

// newHandle is "hk-" and twelve random characters: opaque, short enough to
// paste, and unguessable enough that a handle is not a way to read someone
// else's replies (not that the socket lets anyone else in).
func newHandle() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make handle: %w", err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "hk-" + string(b), nil
}
