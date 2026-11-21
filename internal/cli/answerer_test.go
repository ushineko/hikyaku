package cli_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/persona"
	"github.com/ushineko/hikyaku/internal/slacktest"
)

const allowedOther = "U00000002"

// fakeRunner stands in for Claude Code. Each run calls answer, which may
// block until the run's context ends.
type fakeRunner struct {
	mu      sync.Mutex
	prompts []string
	answer  func(ctx context.Context, prompt string) (answerer.Result, error)
}

func (f *fakeRunner) Run(ctx context.Context, prompt string) (answerer.Result, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	answer := f.answer
	f.mu.Unlock()
	return answer(ctx, prompt)
}

func (f *fakeRunner) runs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

// plainRunner is the runner for runs that read nothing (spec 009). It
// records into the same fake, marking each prompt, so a test can tell
// which runner a question went to.
type plainRunner struct{ r *fakeRunner }

const plainRunnerMark = "[run by the plain runner]\n"

func (p plainRunner) Run(ctx context.Context, prompt string) (answerer.Result, error) {
	return p.r.Run(ctx, plainRunnerMark+prompt)
}

func answers(text string, cost float64) func(context.Context, string) (answerer.Result, error) {
	return func(context.Context, string) (answerer.Result, error) {
		return answerer.Result{Outcome: answerer.Answered, Text: text, CostUSD: cost, Detail: "success"}, nil
	}
}

// blocks never answers: the run ends only when its context does.
func blocks(ctx context.Context, _ string) (answerer.Result, error) {
	<-ctx.Done()
	return answerer.Result{}, ctx.Err()
}

func answererHarness(t *testing.T, settings string) (*harness, *fakeRunner) {
	t.Helper()
	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.settings, []byte("owner: "+owner+"\n"+settings), 0o600))
	r := &fakeRunner{answer: answers("the answer", 0.1)}
	h.runner, h.plainRunner = r, plainRunner{r}
	h.serve()
	return h, r
}

func switchAnswerer(t *testing.T, h *harness, to string) string {
	t.Helper()
	code, out, errOut := h.run("", "answerer", to)
	require.Equal(t, 0, code, errOut)
	return out
}

// postIn waits for a post in thread ts whose text satisfies ok.
func (h *harness) postIn(ts string, ok func(string) bool) slacktest.Post {
	h.t.Helper()
	var found slacktest.Post
	require.Eventually(h.t, func() bool {
		for _, p := range h.fake.Posts() {
			if p.ThreadTS == ts && ok(p.Text) {
				found = p
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "no matching post in thread %s: %+v", ts, h.fake.Posts())
	return found
}

// started is any of the notes a run starts with.
func started(t string) bool { return slices.Contains(persona.Default().Started, t) }

func is(s string) func(string) bool { return func(t string) bool { return t == s } }
func startsWith(s string) func(string) bool {
	return func(t string) bool { return strings.HasPrefix(t, s) }
}

// noRunFor fails if the runner is called within a short wait.
func noRunFor(t *testing.T, r *fakeRunner, before int) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, r.runs(), before, "a run was started")
}

// AC2: off, the owner's DM gets "No agent is listening"; on, it starts one
// run whose answer goes in the question's thread. Someone else's DM, a
// channel message and a DM a listener takes start nothing.
func TestTheAnswererAnswersOnlyTheOwnerWhenOn(t *testing.T) {
	h, r := answererHarness(t, "allowed_repliers: ["+owner+", "+allowedOther+"]\n")
	h.origin = "hikyaku · main · testhost"

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "release status?")
	h.postIn(ts, is("No agent is listening, so this was not delivered."))
	noRunFor(t, r, 0)

	assert.Contains(t, switchAnswerer(t, h, "on"), "answerer: on")
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "release status?")
	h.postIn(ts, started)
	got := h.postIn(ts, is("the answer"))
	assert.Equal(t, slacktest.DMChannel, got.Channel)
	require.Len(t, footer(got), 1)
	assert.True(t, strings.HasPrefix(footer(got)[0], "answerer · "), footer(got))
	assert.Contains(t, footer(got)[0], "automated answer")
	require.Len(t, r.runs(), 1)
	assert.Contains(t, r.runs()[0], "\nrelease status?\n--- end of question ")

	h.fake.Say(t, slacktest.DMChannel, "im", allowedOther, "and mine?")
	h.fake.Say(t, slacktest.DMChannel, "im", stranger, "and mine?")
	h.fake.Say(t, "C00000001", "channel", owner, "in a channel")
	noRunFor(t, r, 1)

	l := startListen(h, "--name", "agent")
	waitListening(t, h, "agent")
	h.fake.Say(t, slacktest.DMChannel, "im", owner, "for the agent")
	require.Equal(t, 0, l.result(t))
	assert.Contains(t, l.out.String(), "for the agent")
	noRunFor(t, r, 1)
}

// AC4: `off` during a run ends it and says so; the switch survives a
// restart.
func TestSwitchingOffStopsARunAndLasts(t *testing.T) {
	h, r := answererHarness(t, "")
	r.answer = blocks
	switchAnswerer(t, h, "on")
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "a long one")
	h.postIn(ts, started)
	assert.Contains(t, switchAnswerer(t, h, "off"), "answerer: off")
	h.postIn(ts, is("Stopped: the answerer was switched off."))

	h.restart()
	_, out, _ := h.run("", "status")
	assert.Contains(t, out, "answerer: off")
	switchAnswerer(t, h, "on")
	h.restart()
	_, out, _ = h.run("", "status")
	assert.Contains(t, out, "answerer: on")
}

// AC5, AC7: one run at a time, three waiting, the next one turned away;
// status shows it all.
func TestQuestionsQueueAndTheQueueIsShort(t *testing.T) {
	h, r := answererHarness(t, "")
	release := make(chan struct{})
	r.answer = func(ctx context.Context, p string) (answerer.Result, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return answerer.Result{}, ctx.Err()
		}
		return answerers(p)
	}
	switchAnswerer(t, h, "on")
	var ts []string
	for i := range 5 {
		ts = append(ts, h.fake.Say(t, slacktest.DMChannel, "im", owner, fmt.Sprintf("q%d", i)))
		if i == 0 {
			h.postIn(ts[0], started)
		}
	}
	h.postIn(ts[1], is("Queued: 1 ahead of it."))
	h.postIn(ts[2], is("Queued: 2 ahead of it."))
	h.postIn(ts[3], is("Queued: 3 ahead of it."))
	h.postIn(ts[4], startsWith("Busy:"))
	_, out, _ := h.run("", "status")
	assert.Contains(t, out, "answerer: on, answering, 3 queued; $0.00 of $5.00 spent today")

	close(release)
	for i := range 4 {
		h.postIn(ts[i], is(fmt.Sprintf("answer to q%d", i)))
	}
	assert.Len(t, r.runs(), 4, "the turned-away question never ran")
	// The answer is posted a moment before the run is marked done.
	require.Eventually(t, func() bool {
		_, out, _ = h.run("", "status")
		return strings.Contains(out, "answerer: on; $0.40 of $5.00 spent today")
	}, 5*time.Second, 10*time.Millisecond, "status: %s", out)
}

func answerers(prompt string) (answerer.Result, error) {
	i := strings.LastIndex(prompt, ") ---\n")
	q := strings.SplitN(prompt[i+len(") ---\n"):], "\n", 2)[0]
	return answerer.Result{Outcome: answerer.Answered, Text: "answer to " + q, CostUSD: 0.1, Detail: "success"}, nil
}

// AC5: a run past its time limit is ended and charged its whole budget;
// past the day's cap nothing runs until the next day; a run past its
// budget says so.
func TestTheAnswererKeepsToItsLimits(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	h := newHarness(t)
	h.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	require.NoError(t, os.WriteFile(h.settings, []byte("owner: "+owner+
		"\nanswerer:\n  run_timeout: 300ms\n  day_budget_usd: 2\n"), 0o600))
	r := &fakeRunner{answer: blocks}
	h.runner = r
	h.serve()
	switchAnswerer(t, h, "on")

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "slow")
	h.postIn(ts, is("Stopped: the answer ran out of time (300ms)."))
	_, out, _ := h.run("", "status")
	assert.Contains(t, out, "$0.50 of $2.00 spent today", "a run that was ended is charged its whole budget")

	r.mu.Lock()
	r.answer = func(context.Context, string) (answerer.Result, error) {
		return answerer.Result{Outcome: answerer.OverBudget, CostUSD: 0.6, Detail: "error_max_budget_usd"}, nil
	}
	r.mu.Unlock()
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "big")
	h.postIn(ts, startsWith("Stopped: the answer reached its budget ($0.50)"))

	r.mu.Lock()
	r.answer = answers("fine", 0.9)
	r.mu.Unlock()
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "one more")
	h.postIn(ts, is("fine"))
	runs := len(r.runs())
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "over the cap")
	h.postIn(ts, startsWith("Not answered: the answerer has used today's budget ($2.00)"))
	noRunFor(t, r, runs)

	mu.Lock()
	now = now.Add(24 * time.Hour)
	mu.Unlock()
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "next day")
	h.postIn(ts, is("fine"))
}

// AC6: an answer cannot mention anyone, and Slack does not fetch its links.
func TestAnAnswerMentionsNobodyAndFetchesNothing(t *testing.T) {
	h, r := answererHarness(t, "")
	r.answer = answers("<!channel> see <@U00000000> in <#C00000001> and <https://example.com/x?d=secret|this>", 0.1)
	switchAnswerer(t, h, "on")
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "q")
	got := h.postIn(ts, startsWith("&lt;!channel>"))
	assert.Equal(t, "&lt;!channel> see &lt;@U00000000> in &lt;#C00000001> and <https://example.com/x?d=secret|this>", got.Text)
	assert.Equal(t, "false", got.UnfurlLinks)
	assert.Equal(t, "false", got.UnfurlMedia)
}

// AC10: a reply in an answer's thread is a follow-up that carries the
// thread so far; only the owner's count; ten per thread.
func TestAFollowUpCarriesTheThread(t *testing.T) {
	h, r := answererHarness(t, "allowed_repliers: ["+owner+", "+allowedOther+"]\n")
	r.answer = func(_ context.Context, p string) (answerer.Result, error) { return answerers(p) }
	switchAnswerer(t, h, "on")
	root := h.fake.Say(t, slacktest.DMChannel, "im", owner, "first")
	h.postIn(root, is("answer to first"))

	h.fake.Reply(t, slacktest.DMChannel, root, owner, "second")
	h.postIn(root, is("answer to second"))
	follow := r.runs()[1]
	assert.Contains(t, follow, "Earlier in this thread:")
	assert.Contains(t, follow, "\nfirst\n--- end of question ")
	assert.Contains(t, follow, "\nanswer to first\n--- end of answer ")
	assert.Contains(t, follow, "The follow-up question:\n--- question ")

	h.fake.Reply(t, slacktest.DMChannel, root, allowedOther, "mine?")
	h.fake.Reply(t, slacktest.DMChannel, root, stranger, "mine?")
	noRunFor(t, r, 2)

	for i := 3; i <= 11; i++ {
		q := fmt.Sprintf("q%d", i)
		h.fake.Reply(t, slacktest.DMChannel, root, owner, q)
		h.postIn(root, is("answer to "+q))
	}
	h.fake.Reply(t, slacktest.DMChannel, root, owner, "one too many")
	h.postIn(root, startsWith("This thread has had its ten follow-ups."))
	noRunFor(t, r, 11)
}

// AC11: an answer leaves without secrets or contact details, says when a
// secret was taken out, and a follow-up sees only what was posted.
func TestAnAnswerLeavesWithoutSecrets(t *testing.T) {
	h, r := answererHarness(t, "")
	key := "glpat-" + strings.Repeat("Q7", 12)
	r.answer = answers("The token is "+key+"; ask jane.doe@example.com.", 0.1)
	switchAnswerer(t, h, "on")
	root := h.fake.Say(t, slacktest.DMChannel, "im", owner, "what's in the config?")
	got := h.postIn(root, startsWith("The token is"))
	assert.Equal(t, "The token is [redacted]; ask [email].\n\n_Something that looked like a secret was removed from this answer._", got.Text)

	r.answer = func(_ context.Context, p string) (answerer.Result, error) { return answerers(p) }
	h.fake.Reply(t, slacktest.DMChannel, root, owner, "and?")
	h.postIn(root, is("answer to and?"))
	assert.NotContains(t, r.runs()[1], key)
	assert.NotContains(t, r.runs()[1], "jane.doe@example.com")
}
