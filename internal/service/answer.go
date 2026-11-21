package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/atomicfile"
	"github.com/ushineko/hikyaku/internal/config"
	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/slackio"
)

// Limits on the answerer's queue and threads (spec 007, R5 and R9).
const (
	maxQueued    = 3
	maxFollowUps = 10
)

// The bot's notes about the answerer, in a question's thread.
const (
	noteBusy      = "Busy: three questions are already waiting. Ask again in a few minutes."
	noteOff       = "Not answered: the answerer was switched off."
	noteStopped   = "Stopped: the answerer was switched off."
	noteFailed    = "The answer failed. The service's log says how."
	noteRedacted  = "Something that looked like a secret was removed from this answer."
	noteFollowUps = "This thread has had its ten follow-ups. Ask a new question at the top level."
)

// errSwitchedOff is why a run is cancelled by `answerer off`.
var errSwitchedOff = errors.New("the answerer was switched off")

// answerState is what survives a restart: whether the answerer is on, and
// what today has cost.
type answerState struct {
	On    bool    `json:"on"`
	Day   string  `json:"day,omitempty"`
	Spent float64 `json:"spent_usd,omitempty"`
}

// question is one question waiting for, or having, a run.
type question struct {
	channel string
	// root is the ts of the owner's top-level message: the thread every
	// answer and follow-up goes in.
	root    string
	text    string
	earlier []answerer.Turn
}

// exchange is an answered thread, kept for follow-ups.
type exchange struct {
	turns     []answerer.Turn
	followUps int
}

// desk is the answerer's state in the service.
type desk struct {
	runner   answerer.Runner
	settings config.Answerer
	path     string
	from     string

	mu      sync.Mutex
	st      answerState
	queue   []question
	running bool
	cancel  context.CancelCauseFunc
	threads map[string]*exchange
	wake    chan struct{}
}

func newDesk(r answerer.Runner, settings config.Answerer, path string) (*desk, error) {
	d := &desk{runner: r, settings: settings, path: path, threads: map[string]*exchange{}, wake: make(chan struct{}, 1)}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	d.from = " · " + host + " · an automated answer from the sources, which may be wrong"
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return d, nil
	case err != nil:
		return nil, fmt.Errorf("read answerer state: %w", err)
	}
	if err := json.Unmarshal(b, &d.st); err != nil {
		return nil, fmt.Errorf("parse answerer state %s: %w", path, err)
	}
	return d, nil
}

// saveLocked writes the state. d.mu is held.
func (d *desk) saveLocked() error {
	b, err := json.Marshal(d.st)
	if err != nil {
		return fmt.Errorf("encode answerer state: %w", err)
	}
	return atomicfile.Write(d.path, b)
}

// todayLocked starts a new day's spending when the date has changed.
func (d *desk) todayLocked(now time.Time) {
	if day := now.Local().Format(time.DateOnly); d.st.Day != day {
		d.st.Day, d.st.Spent = day, 0
	}
}

// answerSwitch is `hikyaku answerer on|off|status` (R1).
func (s *Service) answerSwitch(ctx context.Context, req ipc.Request) ipc.Response {
	d := s.desk
	if d == nil {
		return ipc.Fail(ipc.CodeNotConfigured, "this service has no answerer")
	}
	switch req.Switch {
	case "":
	case "on", "off":
		on := req.Switch == "on"
		d.mu.Lock()
		changed := d.st.On != on
		d.st.On = on
		err := d.saveLocked()
		var dropped []question
		if !on {
			dropped, d.queue = d.queue, nil
			if d.cancel != nil {
				d.cancel(errSwitchedOff)
			}
		}
		d.mu.Unlock()
		if err != nil {
			s.log.Error("save answerer state", "err", err)
			return ipc.Fail(ipc.CodeInternal, s.redact.String(err.Error()))
		}
		if changed {
			s.log.Info("answerer switched", "on", on)
		}
		for _, q := range dropped {
			s.note(ctx, q, noteOff)
		}
	default:
		return ipc.Fail(ipc.CodeBadRequest, "switch the answerer on or off")
	}
	st := s.answerStatus()
	return ipc.Response{OK: true, Status: &ipc.Status{Answerer: st}}
}

func (s *Service) answerStatus() ipc.AnswererStatus {
	d := s.desk
	if d == nil {
		return ipc.AnswererStatus{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.todayLocked(s.now())
	return ipc.AnswererStatus{
		On: d.st.On, Running: d.running, Queued: len(d.queue),
		Spent: d.st.Spent, Cap: d.settings.DayBudget(),
	}
}

// offerQuestion gives the answerer a top-level DM that no listener took
// (R2). It reports whether the answerer took it.
func (s *Service) offerQuestion(ctx context.Context, channel, ts, user, text string) bool {
	if s.desk == nil || user != s.settings.Owner {
		return false
	}
	return s.enqueue(ctx, question{channel: channel, root: ts, text: text}, true)
}

// offerFollowUp gives the answerer a reply in one of its answers' threads,
// when no agent waits on that thread (R9).
func (s *Service) offerFollowUp(ctx context.Context, channel, root, user, text string) {
	d := s.desk
	if d == nil || user != s.settings.Owner {
		return
	}
	d.mu.Lock()
	ex := d.threads[root]
	if ex == nil || !d.st.On {
		d.mu.Unlock()
		return
	}
	if ex.followUps >= maxFollowUps {
		d.mu.Unlock()
		s.note(ctx, question{channel: channel, root: root}, noteFollowUps)
		return
	}
	ex.followUps++
	earlier := slices.Clone(ex.turns)
	d.mu.Unlock()
	s.enqueue(ctx, question{channel: channel, root: root, text: text, earlier: earlier}, false)
}

// enqueue queues q, telling the owner when it has to wait or cannot (R5).
// onlyIfOn makes it refuse when the answerer is off.
func (s *Service) enqueue(ctx context.Context, q question, onlyIfOn bool) bool {
	d := s.desk
	d.mu.Lock()
	if onlyIfOn && !d.st.On {
		d.mu.Unlock()
		return false
	}
	if len(d.queue) >= maxQueued {
		d.mu.Unlock()
		s.note(ctx, q, noteBusy)
		return true
	}
	d.queue = append(d.queue, q)
	ahead := len(d.queue) - 1
	if d.running {
		ahead++
	}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
	if ahead > 0 {
		s.note(ctx, q, fmt.Sprintf("Queued: %d ahead of it.", ahead))
	}
	return true
}

// answerLoop runs one question at a time until ctx ends.
func (s *Service) answerLoop(ctx context.Context) {
	d := s.desk
	for {
		d.mu.Lock()
		if len(d.queue) == 0 {
			d.mu.Unlock()
			select {
			case <-d.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		q := d.queue[0]
		d.queue = d.queue[1:]
		runCtx, cancel := context.WithCancelCause(ctx)
		d.running, d.cancel = true, cancel
		d.mu.Unlock()

		s.answer1(runCtx, ctx, q)

		cancel(nil)
		d.mu.Lock()
		d.running, d.cancel = false, nil
		d.mu.Unlock()
	}
}

// answer1 runs one question and posts what came of it. runCtx is cancelled
// by `off`; ctx is the service's, for posting afterwards.
func (s *Service) answer1(runCtx, ctx context.Context, q question) {
	d := s.desk
	d.mu.Lock()
	d.todayLocked(s.now())
	spent, dayCap := d.st.Spent, d.settings.DayBudget()
	d.mu.Unlock()
	if spent >= dayCap {
		s.note(ctx, q, fmt.Sprintf("Not answered: the answerer has used today's budget ($%.2f). It answers again after midnight.", dayCap))
		return
	}
	p := s.persona.Get()
	s.note(ctx, q, p.StartedNote())

	prompt, err := answerer.Prompt(q.text, q.earlier, p.Voice)
	if err != nil {
		s.log.Error("answerer prompt", "err", err)
		s.note(ctx, q, noteFailed)
		return
	}
	timeout := d.settings.Timeout()
	tctx, cancel := context.WithTimeout(runCtx, timeout)
	start := time.Now()
	res, err := d.runner.Run(tctx, prompt)
	timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded)
	cancel()
	if err != nil {
		// A run that was ended, or broke, reported no cost; count what it
		// may have used, so the day's total does not run low.
		res.CostUSD = d.settings.RunBudget()
	}

	d.mu.Lock()
	d.todayLocked(s.now())
	d.st.Spent += res.CostUSD
	if serr := d.saveLocked(); serr != nil {
		s.log.Error("save answerer state", "err", serr)
	}
	d.mu.Unlock()
	s.log.Info("answerer run", "duration", time.Since(start).Round(time.Millisecond),
		"cost_usd", res.CostUSD, "cost_reported", err == nil, "denied", len(res.Denials), "ended", res.Detail,
		"follow_up", len(q.earlier) > 0)
	if len(res.Denials) > 0 {
		s.log.Info("answerer run denied", "calls", res.Denials)
	}

	switch {
	case errors.Is(context.Cause(runCtx), errSwitchedOff):
		s.note(ctx, q, noteStopped)
	case timedOut:
		s.note(ctx, q, fmt.Sprintf("Stopped: the answer ran out of time (%s).", timeout))
	case err != nil:
		s.log.Warn("answerer run failed", "err", s.redact.String(err.Error()))
		s.note(ctx, q, noteFailed)
	case res.Outcome == answerer.OverBudget:
		s.note(ctx, q, fmt.Sprintf("Stopped: the answer reached its budget ($%.2f) before it was done. "+
			"A narrower question costs less.", d.settings.RunBudget()))
	case res.Outcome != answerer.Answered || strings.TrimSpace(res.Text) == "":
		s.note(ctx, q, noteFailed)
	default:
		s.postAnswer(ctx, q, res.Text)
	}
}

// postAnswer posts an answer in its question's thread (R6), with secrets
// and contact details removed (R10), and keeps the exchange for follow-ups.
func (s *Service) postAnswer(ctx context.Context, q question, text string) {
	d := s.desk
	text, n := answerer.Scrub(text)
	if r := s.redact.String(text); r != text {
		text = r
		n.Secrets++
	}
	if n != (answerer.Scrubbed{}) {
		s.log.Info("answerer scrubbed an answer", "secrets", n.Secrets, "emails", n.Emails, "phones", n.Phones)
	}
	body := slackio.Neutralize(text)
	if n.Secrets > 0 {
		body += "\n\n_" + noteRedacted + "_"
	}
	posted, ts, err := s.poster.Post(ctx, q.channel, slackio.Outgoing{
		Text: body, From: s.answererName() + d.from, ThreadTS: q.root, NoUnfurl: true,
	})
	if err != nil {
		s.log.Warn("post an answer", "err", s.redact.String(err.Error()))
		return
	}
	if _, err := s.store.Add(posted, ts, q.root); err != nil {
		s.log.Error("record answer", "err", err)
	}
	d.mu.Lock()
	ex := d.threads[q.root]
	if ex == nil {
		ex = &exchange{}
		d.threads[q.root] = ex
	}
	ex.turns = append(ex.turns, answerer.Turn{Question: q.text, Answer: text})
	d.mu.Unlock()
}

// note posts one of the bot's notes in a question's thread.
func (s *Service) note(ctx context.Context, q question, text string) {
	s.post(ctx, q.channel, slackio.Outgoing{Text: text, ThreadTS: q.root})
}

// answererName is the persona's name, or "answerer" (spec 008, R5).
func (s *Service) answererName() string {
	if n := s.persona.Get().Name; n != "" {
		return n
	}
	return answererPrefix
}
