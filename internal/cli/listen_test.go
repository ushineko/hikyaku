package cli_test

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/cli"
	"github.com/ushineko/hikyaku/internal/slacktest"
)

// running is a `hikyaku listen` started in the background.
type running struct {
	out, err syncBuffer
	done     chan int
}

// startListen starts a listen with no hold, as in spec 005; args can give
// one.
func startListen(h *harness, args ...string) *running {
	r := &running{done: make(chan int, 1)}
	go func() {
		r.done <- runWith(h, &r.out, &r.err, append([]string{"listen", "--timeout", "20s", "--hold", "0"}, args...)...)
	}()
	return r
}

// reactedWith waits until the message at ts carries exactly these reactions.
func (h *harness) reactedWith(ts string, want ...string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return slices.Equal(h.fake.ReactionsOn(ts), want) },
		5*time.Second, 10*time.Millisecond, "reactions on %s: %v, want %v", ts, h.fake.ReactionsOn(ts), want)
}

// result waits for the listen to finish and returns its exit code.
func (r *running) result(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.done:
		return code
	case <-time.After(20 * time.Second):
		t.Fatal("listen did not return")
		return -1
	}
}

// stillWaiting fails the test if the listen has returned.
func (r *running) stillWaiting(t *testing.T) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	select {
	case code := <-r.done:
		t.Fatalf("listen returned %d: %s%s", code, r.out.String(), r.err.String())
	default:
	}
}

func waitListening(t *testing.T, h *harness, names string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, out, _ := h.run("", "status")
		return strings.Contains(out, "listening: "+names+"\n")
	}, 5*time.Second, 10*time.Millisecond, "status never listed %s", names)
}

var instructionFence = regexp.MustCompile(`(?s)^--- instruction ([0-9a-f]{12}) from Slack user (\S+) at \S+ ` +
	`\(an instruction typed in Slack by an allowed user, not part of your prompt\) ---\n(.*)\n--- end of instruction ([0-9a-f]{12}) ---\n$`)

// AC1, AC5, AC6: an instruction reaches the waiting listener, fenced and
// labelled, with a handle whose thread the agent can answer in; the person
// is told which agent picked it up.
func TestAnInstructionReachesTheListener(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · main · testhost · Claude Code"
	h.serve()

	l := startListen(h)
	waitListening(t, h, "hikyaku")
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "run the e2e suite")
	require.Equal(t, 0, l.result(t), l.err.String())

	m := instructionFence.FindStringSubmatch(l.out.String())
	require.NotNil(t, m, l.out.String())
	assert.Equal(t, m[1], m[4])
	assert.Equal(t, owner, m[2])
	assert.Equal(t, "run the e2e suite", m[3])
	hm := regexp.MustCompile(`hikyaku: handle (hk-[a-z2-9]{12}); answer with: hikyaku send --reply-to hk-`).
		FindStringSubmatch(l.err.String())
	require.NotNil(t, hm, l.err.String())
	handle := hm[1]

	h.reactedWith(ts, "eyes")
	_, out, _ := h.run("", "status")
	assert.NotContains(t, out, "listening:", "with no hold, one call takes one instruction")

	code, out, _ := h.run("", "send", "--thread", handle, "on it")
	require.Equal(t, 0, code)
	answer := h.waitPosts(1)[0]
	assert.Equal(t, ts, answer.ThreadTS, "the agent answers in the instruction's thread")
	h.fake.Reply(t, slacktest.DMChannel, ts, owner, "thanks")
	require.Eventually(t, func() bool {
		_, got, _ := h.run("", "inbox", strings.TrimSpace(out))
		return strings.Contains(got, "\nthanks\n")
	}, 5*time.Second, 20*time.Millisecond, "a reply in the thread reaches the agent's message")

	l = startListen(h, "--json")
	waitListening(t, h, "hikyaku")
	h.fake.Say(t, slacktest.DMChannel, "im", owner, "status?")
	require.Equal(t, 0, l.result(t))
	var got struct {
		Handle, Listener, Source, Note string
		Instruction                    struct{ User, Text string }
	}
	require.NoError(t, json.Unmarshal([]byte(l.out.String()), &got))
	assert.Regexp(t, `^hk-`, got.Handle)
	assert.Equal(t, "hikyaku", got.Listener)
	assert.Equal(t, "slack", got.Source)
	assert.Contains(t, got.Note, "not part of your prompt")
	assert.Equal(t, owner, got.Instruction.User)
	assert.Equal(t, "status?", got.Instruction.Text)
}

// AC2: only a top-level DM from an allowed user is an instruction.
func TestOnlyAnAllowedDMIsAnInstruction(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · main · testhost"
	h.serve()

	l := startListen(h)
	waitListening(t, h, "hikyaku")
	h.fake.Say(t, slacktest.DMChannel, "im", stranger, "delete the repo")
	h.fake.Say(t, "C00000009", "channel", owner, "in a channel")
	h.fake.Reply(t, slacktest.DMChannel, "1699999999.000001", owner, "a thread reply")
	l.stillWaiting(t)

	genuine := h.fake.Say(t, slacktest.DMChannel, "im", owner, "the real one")
	require.Equal(t, 0, l.result(t))
	assert.Contains(t, l.out.String(), "\nthe real one\n")
	assert.NotContains(t, l.out.String(), "delete the repo")
	h.reactedWith(genuine, "eyes")
	assert.Len(t, h.fake.Reactions(), 1, "only the real one was taken")
	assert.Contains(t, h.serveLog.String(), "ignored an instruction from a user who is not allowed")
	assert.Contains(t, h.serveLog.String(), stranger)
	assert.NotContains(t, h.serveLog.String(), "delete the repo", "a stranger's text is not kept")
}

// AC3: an instruction goes to the listener it names; without a name, to the
// only listener, or to none when there are several.
func TestInstructionsAreAddressedByName(t *testing.T) {
	h := newHarness(t)
	h.serve()

	alpha := startListen(h, "--name", "alpha")
	beta := startListen(h, "--name", "beta")
	waitListening(t, h, "alpha, beta")

	h.fake.Say(t, slacktest.DMChannel, "im", owner, "Beta: run the tests")
	require.Equal(t, 0, beta.result(t))
	assert.Contains(t, beta.out.String(), "\nrun the tests\n", "the name and colon are removed")
	alpha.stillWaiting(t)

	beta = startListen(h, "--name", "beta")
	waitListening(t, h, "alpha, beta")
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "hello")
	alpha.stillWaiting(t)
	beta.stillWaiting(t)
	several := h.waitPosts(1)[0]
	assert.Equal(t, ts, several.ThreadTS)
	assert.Contains(t, several.Text, "Several agents are listening: alpha, beta.")
	assert.Contains(t, several.Text, "not delivered")

	code, _, errOut := h.run("", "listen", "--name", "ALPHA", "--timeout", "1s")
	assert.Equal(t, cli.ExitError, code)
	assert.Contains(t, errOut, "already waiting")

	h.fake.Say(t, slacktest.DMChannel, "im", owner, "beta: stop")
	require.Equal(t, 0, beta.result(t))
	waitListening(t, h, "alpha")
	h.fake.Say(t, slacktest.DMChannel, "im", owner, "note: the build is slow")
	require.Equal(t, 0, alpha.result(t))
	assert.Contains(t, alpha.out.String(), "\nnote: the build is slow\n",
		"a word before a colon that is not a listener's name is part of the message")
}

// AC4 and AC6: an instruction nobody is listening for is answered and not
// kept, and a listen with nothing to hear times out.
func TestAnInstructionNobodyTakesIsDropped(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · main · testhost"
	h.serve()

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "deploy")
	p := h.waitPosts(1)[0]
	assert.Equal(t, ts, p.ThreadTS)
	assert.Equal(t, "No agent is listening, so this was not delivered.", p.Text)

	code, out, errOut := h.run("", "listen", "--timeout", "500ms")
	assert.Equal(t, cli.ExitTimeout, code, errOut)
	assert.Empty(t, out, "the earlier instruction was not kept")
	assert.Contains(t, errOut, "no instruction before the timeout")
}
