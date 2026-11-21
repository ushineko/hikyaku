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
	noteUserCap   = "That's my limit for you today. Ask again tomorrow."
)

// userDayBudget is what one user's runs may cost in a day (spec 009, R6).
const userDayBudget = 1.00

// errSwitchedOff is why a run is cancelled by `answerer off`.
var errSwitchedOff = errors.New("the answerer was switched off")

// answerState is what survives a restart: whether the answerer is on, and
// what today has cost.
type answerState struct {
	On    bool    `json:"on"`
	Day   string  `json:"day,omitempty"`
	Spent float64 `json:"spent_usd,omitempty"`
	// SpentBy is today's spending by user other than an admin (spec 009).
	SpentBy map[string]float64 `json:"spent_by,omitempty"`
}

// question is one question waiting for, or having, a run.
type question struct {
	channel string
	// root is the ts of the owner's top-level message: the thread every
	// answer and follow-up goes in.
	root    string
	text    string
	earlier []answerer.Turn
	// asker is who asked; plain runs read nothing (spec 009, R3).
	asker string
	plain bool
	// msgTS is the asking message in a channel, which shows progress as
	// reactions; topLevel posts the answer beside it rather than in a
	// thread; thread is the channel thread so far (spec 009, R2).
	msgTS    string
	topLevel bool
	thread   []answerer.Line
}

// exchange is an answered thread, kept for follow-ups.
type exchange struct {
	turns     []answerer.Turn
	followUps int
	// asker and channel are who asked a channel question answered at the
	// top level, and where: in that answer's thread, the asker may reply
	// without a mention (spec 009, R2).
	asker, channel string
}

// desk is the answerer's state in the service.
type desk struct {
	runner answerer.Runner
	// plain runs with no tools (spec 009, R3).
	plain    answerer.Runner
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

func newDesk(r, plain answerer.Runner, settings config.Answerer, path string) (*desk, error) {
	if plain == nil {
		plain = r
	}
	d := &desk{runner: r, plain: plain, settings: settings, path: path, threads: map[string]*exchange{},
		wake: make(chan struct{}, 1)}
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
		d.st.Day, d.st.Spent, d.st.SpentBy = day, 0, nil
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
	users := 0.0
	for _, v := range d.st.SpentBy {
		users += v
	}
	return ipc.AnswererStatus{
		On: d.st.On, Running: d.running, Queued: len(d.queue),
		Spent: d.st.Spent, Cap: d.settings.DayBudget(), UsersSpent: users,
	}
}

// offerQuestion gives the answerer a top-level DM that no listener took
// (spec 007, R2): an admin's gets the full answerer, a user's with the DM
// grant a plain one (spec 009, R2). It reports whether the answerer took
// it.
func (s *Service) offerQuestion(ctx context.Context, channel, ts, user, text string) bool {
	if s.desk == nil {
		return false
	}
	q := question{channel: channel, root: ts, text: text, asker: user}
	switch {
	case s.isAdmin(user):
	case s.mayDM(user):
		q.plain = true
	default:
		return false
	}
	return s.enqueue(ctx, q, true)
}

// offerFollowUp gives the answerer a reply in one of its answers' threads,
// when no agent waits on that thread (spec 007, R9). inDM says where the thread is.
// In a channel, a reply is a question only with a mention, which
// channelMessage handles (spec 009, R2), so this is for DMs.
func (s *Service) offerFollowUp(ctx context.Context, channel, root, user, text string) {
	d := s.desk
	if d == nil {
		return
	}
	q := question{channel: channel, root: root, text: text, asker: user}
	switch {
	case s.isAdmin(user):
	case s.mayDM(user):
		q.plain = true
	default:
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
	q.earlier = slices.Clone(ex.turns)
	d.mu.Unlock()
	s.enqueue(ctx, q, false)
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
	userSpent := d.st.SpentBy[q.asker]
	d.mu.Unlock()
	if spent >= dayCap {
		s.note(ctx, q, fmt.Sprintf("Not answered: the answerer has used today's budget ($%.2f). It answers again after midnight.", dayCap))
		return
	}
	if !s.isAdmin(q.asker) && userSpent >= userDayBudget {
		s.note(ctx, q, noteUserCap)
		return
	}
	p := s.persona.Get()
	if q.msgTS != "" {
		go s.reactions(q.channel, q.msgTS, reactTaken, "", slackio.Outgoing{})
	} else {
		s.note(ctx, q, slackio.Neutralize(p.StartedNote())) // the persona's words, not the service's
	}

	makePrompt, runner := answerer.Prompt, d.runner
	if q.plain {
		makePrompt, runner = answerer.PlainPrompt, d.plain
	}
	prompt, err := makePrompt(q.text, q.earlier, p.VoiceForAnswer(), q.thread...)
	if err != nil {
		s.log.Error("answerer prompt", "err", err)
		s.note(ctx, q, noteFailed)
		return
	}
	timeout := d.settings.Timeout()
	tctx, cancel := context.WithTimeout(runCtx, timeout)
	start := time.Now()
	res, err := runner.Run(tctx, prompt)
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
	if !s.isAdmin(q.asker) && q.asker != "" {
		if d.st.SpentBy == nil {
			d.st.SpentBy = map[string]float64{}
		}
		d.st.SpentBy[q.asker] += res.CostUSD
	}
	if serr := d.saveLocked(); serr != nil {
		s.log.Error("save answerer state", "err", serr)
	}
	d.mu.Unlock()
	s.log.Info("answerer run", "duration", time.Since(start).Round(time.Millisecond),
		"cost_usd", res.CostUSD, "cost_reported", err == nil, "denied", len(res.Denials), "ended", res.Detail,
		"follow_up", len(q.earlier) > 0, "plain", q.plain)
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
	text, body := s.clean(text)
	s.publish(ctx, q, text, body)
}

// clean scrubs an answer (spec 007, R10) and returns it and the body to
// post, with mentions neutralized and a line when a secret was removed.
func (s *Service) clean(text string) (string, string) {
	text, paths := s.paths.Generalize(text)
	if paths > 0 {
		s.log.Info("answerer generalized paths in an answer", "paths", paths)
	}
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
	return text, body
}

// publish posts a cleaned answer in its question's thread and keeps the
// exchange for follow-ups.
func (s *Service) publish(ctx context.Context, q question, text, body string) {
	d := s.desk
	thread := q.root
	if q.topLevel {
		thread = ""
	}
	posted, ts, err := s.poster.Post(ctx, q.channel, slackio.Outgoing{
		Text: body, From: s.answererName() + d.from, ThreadTS: thread, NoUnfurl: true,
	})
	if err != nil {
		s.log.Warn("post an answer", "err", s.redact.String(err.Error()))
		return
	}
	if _, err := s.store.Add(posted, ts, thread); err != nil {
		s.log.Error("record answer", "err", err)
	}
	d.mu.Lock()
	key := q.root
	if q.topLevel {
		key = ts
	}
	ex := d.threads[key]
	if ex == nil {
		ex = &exchange{}
		d.threads[key] = ex
	}
	if q.topLevel {
		ex.asker, ex.channel = q.asker, q.channel
	}
	ex.turns = append(ex.turns, turnOf(q, text))
	d.mu.Unlock()
	if q.msgTS != "" {
		go s.reactions(q.channel, q.msgTS, reactAnswered, reactTaken, slackio.Outgoing{})
	}
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
