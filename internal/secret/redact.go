package secret

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

// tokenPattern matches anything shaped like a Slack token, whether or not it
// is one of ours: a token from another app pasted into a message is as
// sensitive as our own.
var tokenPattern = regexp.MustCompile(`\bx(?:ox[abposr]|app)-[A-Za-z0-9-]+`) //nolint:gochecknoglobals // compiled once

// Redactor removes tokens from text: the values it has been told about, and
// anything shaped like a Slack token.
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

// Add registers a value to remove. Empty values are ignored.
func (r *Redactor) Add(v string) {
	if v == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, v)
}

// String returns s with every token replaced by [redacted].
func (r *Redactor) String(s string) string {
	r.mu.RLock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	r.mu.RUnlock()
	return tokenPattern.ReplaceAllString(s, "[redacted]")
}

// Handler wraps a slog.Handler so the message and every string, error and
// stringer attribute pass through the redactor before they are written.
func (r *Redactor) Handler(h slog.Handler) slog.Handler {
	return &redactingHandler{next: h, r: r}
}

type redactingHandler struct {
	next slog.Handler
	r    *Redactor
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.String(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out) //nolint:wrapcheck // a handler passes its successor's error through
}

func (h *redactingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = h.attr(a)
	}
	return &redactingHandler{next: h.next.WithAttrs(clean), r: h.r}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), r: h.r}
}

func (h *redactingHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.r.String(v.String()))
	case slog.KindGroup:
		g := v.Group()
		clean := make([]any, len(g))
		for i, ga := range g {
			clean[i] = h.attr(ga)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, h.r.String(x.Error()))
		case interface{ String() string }:
			return slog.String(a.Key, h.r.String(x.String()))
		}
	}
	return a
}
