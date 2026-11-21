package cli_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/cli"
	"github.com/ushineko/hikyaku/internal/slacktest"
)

// Reactions by Slack's names (spec 006, R5).
const (
	queuedR   = "hourglass_flowing_sand"
	takenR    = "eyes"
	answeredR = "white_check_mark"
	droppedR  = "mailbox_with_no_mail"
)

// startCmd runs any command in the background.
func startCmd(h *harness, args ...string) *running {
	r := &running{done: make(chan int, 1)}
	go func() { r.done <- runWith(h, &r.out, &r.err, args...) }()
	return r
}

var handleLine = regexp.MustCompile(`hikyaku: handle (hk-[a-z2-9]{12})`)

// handleOf is the handle a listen or ask printed on stderr.
func handleOf(t *testing.T, r *running) string {
	t.Helper()
	m := handleLine.FindStringSubmatch(r.err.String())
	require.NotNil(t, m, r.err.String())
	return m[1]
}

// held listens as name with a hold, takes one instruction, and returns its
// ts and handle. The listener is held afterwards.
func held(t *testing.T, h *harness, name, hold, text string) (string, string) {
	t.Helper()
	l := startListen(h, "--name", name, "--hold", hold)
	waitListening(t, h, name)
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, name+": "+text)
	require.Equal(t, 0, l.result(t), l.err.String())
	return ts, handleOf(t, l)
}

func statusLine(h *harness) string {
	_, out, _ := h.run("", "status")
	return out
}

// noteIn waits for a post in thread ts with exactly this text.
func (h *harness) noteIn(ts, text string) slacktest.Post { return h.postIn(ts, is(text)) }

// AC1, AC7: a held listener's messages wait, marked ⏳, and its next listen
// takes the oldest at once, swapping ⏳ for 👀.
func TestAHeldListenerQueuesAndTakes(t *testing.T) {
	h := newHarness(t)
	h.serve()

	first, _ := held(t, h, "alpha", "1m", "first")
	h.reactedWith(first, takenR)
	assert.Contains(t, statusLine(h), "listening: alpha (busy, 0 queued)\n")

	second := h.fake.Say(t, slacktest.DMChannel, "im", owner, "second")
	h.reactedWith(second, queuedR)
	require.Eventually(t, func() bool { return strings.Contains(statusLine(h), "alpha (busy, 1 queued)") },
		5*time.Second, 10*time.Millisecond)

	l := startListen(h, "--name", "alpha", "--hold", "1m")
	require.Equal(t, 0, l.result(t), l.err.String())
	assert.Contains(t, l.out.String(), "\nsecond\n")
	h.reactedWith(second, takenR)
	assert.Empty(t, h.fake.Posts(), "reactions, not notes")
}

// AC2: a held listener counts for routing and its name is resumable; a
// waiting one's is not; "answerer" is no listener's name.
func TestAHeldListenerCountsForRouting(t *testing.T) {
	h := newHarness(t)
	h.serve()

	held(t, h, "alpha", "1m", "first")
	beta := startListen(h, "--name", "beta")
	waitListening(t, h, "alpha (busy, 0 queued), beta")

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "hello")
	assert.Contains(t, h.postIn(ts, startsWith("Several")).Text, "Several agents are listening: alpha, beta.")
	x := h.fake.Say(t, slacktest.DMChannel, "im", owner, "alpha: x")
	h.reactedWith(x, queuedR)
	beta.stillWaiting(t)

	alpha := startListen(h, "--name", "alpha", "--hold", "1m")
	require.Equal(t, 0, alpha.result(t), "a held name is resumed")
	assert.Contains(t, alpha.out.String(), "\nx\n")

	alpha = startListen(h, "--name", "alpha", "--hold", "1m")
	waitListening(t, h, "alpha, beta")
	code, _, errOut := h.run("", "listen", "--name", "alpha", "--timeout", "1s")
	assert.Equal(t, cli.ExitError, code)
	assert.Contains(t, errOut, "already waiting")
	code, _, errOut = h.run("", "listen", "--name", "answerer", "--timeout", "1s")
	assert.Equal(t, cli.ExitError, code)
	assert.Contains(t, errOut, "addresses the answerer")

	h.fake.Say(t, slacktest.DMChannel, "im", owner, "alpha: done")
	require.Equal(t, 0, alpha.result(t))
	h.fake.Say(t, slacktest.DMChannel, "im", owner, "beta: done")
	require.Equal(t, 0, beta.result(t))
}

// AC3: ten wait; the eleventh is turned away and never received.
func TestTheQueueHoldsTen(t *testing.T) {
	h := newHarness(t)
	h.serve()

	held(t, h, "alpha", "1m", "first")
	var last string
	for i := 1; i <= 11; i++ {
		last = h.fake.Say(t, slacktest.DMChannel, "im", owner, fmt.Sprintf("q%d", i))
	}
	h.noteIn(last, "Not delivered: *alpha* already has ten instructions waiting.")
	assert.Contains(t, statusLine(h), "alpha (busy, 10 queued)")

	var got []string
	for range 10 {
		l := startListen(h, "--name", "alpha", "--hold", "1m")
		require.Equal(t, 0, l.result(t))
		got = append(got, strings.Split(l.out.String(), "\n")[1])
	}
	assert.Equal(t, []string{"q1", "q2", "q3", "q4", "q5", "q6", "q7", "q8", "q9", "q10"}, got, "oldest first")
	assert.NotContains(t, h.fake.ReactionsOn(last), queuedR)
}

// AC4: a hold that runs out says what was not delivered and that the
// listener stopped, once; a resumed listener and one that took an
// instruction with no hold say nothing.
func TestAHoldThatEndsSaysSo(t *testing.T) {
	h := newHarness(t)
	h.serve()

	held(t, h, "alpha", "400ms", "first")
	a := h.fake.Say(t, slacktest.DMChannel, "im", owner, "a")
	b := h.fake.Say(t, slacktest.DMChannel, "im", owner, "b")
	h.reactedWith(a, queuedR)
	h.reactedWith(b, queuedR)
	h.noteIn(a, "Not delivered: *alpha* stopped listening.")
	h.noteIn(b, "Not delivered: *alpha* stopped listening.")
	h.reactedWith(a, droppedR)
	h.reactedWith(b, droppedR)
	require.Eventually(t, func() bool { return len(h.fake.MeMessages()) == 1 }, 5*time.Second, 10*time.Millisecond)
	me := h.fake.MeMessages()[0]
	assert.Equal(t, "stopped listening for *alpha*.", me.Text)
	assert.Equal(t, slacktest.DMChannel, me.Channel)
	assert.Empty(t, me.ThreadTS, "top-level, in the DM's flow")
	assert.NotContains(t, statusLine(h), "listening:")

	code, _, _ := h.run("", "listen", "--name", "beta", "--timeout", "200ms", "--hold", "0")
	assert.Equal(t, cli.ExitTimeout, code)
	require.Eventually(t, func() bool { return len(h.fake.MeMessages()) == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "stopped listening for *beta*.", h.fake.MeMessages()[1].Text)

	held(t, h, "gamma", "500ms", "first")
	g := startListen(h, "--name", "gamma") // resumed in time, with no hold of its own
	time.Sleep(800 * time.Millisecond)
	assert.Len(t, h.fake.MeMessages(), 2, "a resumed listener says nothing")
	h.fake.Say(t, slacktest.DMChannel, "im", owner, "gamma: last")
	require.Equal(t, 0, g.result(t))
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, h.fake.MeMessages(), 2, "one that took an instruction with no hold says nothing")
}

// AC5: --reply-to answers beside an instruction, and beside that answer,
// with no origin line, and marks the instruction answered; on another
// message, it answers in its thread; it cannot be combined.
func TestReplyToAnswersWhereThePersonWrote(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · main · testhost · Claude Code"
	h.serve()

	ts, instr := held(t, h, "alpha", "0", "status?")
	h.reactedWith(ts, takenR)
	code, out, errOut := h.run("", "send", "--reply-to", instr, "all green")
	require.Equal(t, 0, code, errOut)
	p := h.waitPosts(1)[0]
	assert.Empty(t, p.ThreadTS, "top-level, beside the instruction")
	assert.Empty(t, footer(p), "no origin line in a conversation")
	h.reactedWith(ts, answeredR)

	code, _, _ = h.run("", "send", "--reply-to", strings.TrimSpace(out), "and more")
	require.Equal(t, 0, code)
	assert.Empty(t, h.waitPosts(2)[1].ThreadTS, "beside an answer that was beside an instruction")

	_, plain, _ := h.run("", "send", "a notification")
	h.waitPosts(3)
	code, _, _ = h.run("", "send", "--reply-to", strings.TrimSpace(plain), "about that")
	require.Equal(t, 0, code)
	assert.NotEmpty(t, h.waitPosts(4)[3].ThreadTS, "in the thread of an ordinary message")

	code, _, errOut = h.run("", "send", "--reply-to", instr, "--thread", instr, "x")
	assert.Equal(t, cli.ExitUsage, code)
	assert.Contains(t, errOut, "--reply-to decides")
	code, _, _ = h.run("", "send", "--reply-to", instr, "--channel", "C00000009", "x")
	assert.Equal(t, cli.ExitUsage, code)
	assert.Len(t, h.fake.Posts(), 4, "neither was posted")
}

// AC6, AC7: an ask beside an instruction takes the person's next top-level
// message as its answer, ahead of the held listener; a reply in its thread
// answers it too.
func TestAConversationWithoutThreads(t *testing.T) {
	h := newHarness(t)
	h.serve()

	_, instr := held(t, h, "alpha", "1m", "deploy it")
	ask := startCmd(h, "ask", "--reply-to", instr, "--timeout", "20s", "which branch?")
	q := h.waitPosts(1)[0]
	assert.Empty(t, q.ThreadTS)
	require.Eventually(t, func() bool { return strings.Contains(statusLine(h), "alpha (in a conversation)") },
		5*time.Second, 10*time.Millisecond)

	answer := h.fake.Say(t, slacktest.DMChannel, "im", owner, "main")
	require.Equal(t, 0, ask.result(t), ask.err.String())
	assert.Contains(t, ask.out.String(), "\nmain\n")
	assert.Contains(t, statusLine(h), "alpha (busy, 0 queued)", "the answer was not queued for the listener")
	assert.Empty(t, h.fake.ReactionsOn(answer))

	ask = startCmd(h, "ask", "--reply-to", instr, "--timeout", "20s", "sure?")
	h.waitPosts(2)
	h.fake.Reply(t, slacktest.DMChannel, h.fake.LastTS(), owner, "yes")
	require.Equal(t, 0, ask.result(t), ask.err.String())
	assert.Contains(t, ask.out.String(), "\nyes\n")
}

// AC8: without reactions:write, the old notes stand in, and the log says
// once what is missing.
func TestWithoutTheReactionsScopeNotesStandIn(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · main · testhost"
	h.serve()
	h.fake.NoReactionScope()

	first, _ := held(t, h, "alpha", "1m", "first")
	picked := h.noteIn(first, "Picked up.")
	assert.Equal(t, []string{h.origin}, footer(picked), "the note says which agent")
	second := h.fake.Say(t, slacktest.DMChannel, "im", owner, "second")
	h.noteIn(second, "Queued: *alpha* is busy with an earlier instruction.")
	assert.Equal(t, 1, strings.Count(h.serveLog.String(), "no reactions:write scope"))
}

// AC9: a /me from an allowed user is a message, top-level or in a thread;
// from anyone else, or from a bot, it is not.
func TestAMeIsAMessage(t *testing.T) {
	h := newHarness(t)
	h.serve()

	l := startListen(h, "--name", "alpha")
	waitListening(t, h, "alpha")
	h.fake.Me(t, slacktest.DMChannel, "", stranger, "deletes the repo")
	h.fake.BotMe(t, slacktest.DMChannel, owner, "alpha stopped listening.")
	l.stillWaiting(t)
	h.fake.Me(t, slacktest.DMChannel, "", owner, "waves")
	require.Equal(t, 0, l.result(t))
	assert.Contains(t, l.out.String(), "\nwaves\n")

	ask := startCmd(h, "ask", "--timeout", "20s", "ready?")
	h.waitPosts(1)
	h.fake.Me(t, slacktest.DMChannel, h.fake.LastTS(), owner, "nods")
	require.Equal(t, 0, ask.result(t), ask.err.String())
	assert.Contains(t, ask.out.String(), "\nnods\n")
}

// AC10: "answerer:" reaches the answerer while an agent listens; with the
// answerer off, the bot says so.
func TestAnswererPrefixBypassesListeners(t *testing.T) {
	h, r := answererHarness(t, "")
	switchAnswerer(t, h, "on")
	l := startListen(h, "--name", "alpha")
	waitListening(t, h, "alpha")

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "answerer: what's the latest release?")
	h.postIn(ts, is("the answer"))
	require.Len(t, r.runs(), 1)
	assert.Contains(t, r.runs()[0], "\nwhat's the latest release?\n--- end of question ")
	l.stillWaiting(t)

	switchAnswerer(t, h, "off")
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "Answerer: still there?")
	h.noteIn(ts, "The answerer is off.")
	l.stillWaiting(t)
	assert.Len(t, r.runs(), 1)

	h.fake.Say(t, slacktest.DMChannel, "im", owner, "alpha: bye")
	require.Equal(t, 0, l.result(t))
}
