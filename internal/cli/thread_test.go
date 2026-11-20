package cli_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/cli"
	"github.com/ushineko/hikyaku/internal/slacktest"
)

// askAndAnswer runs ask with args, answers it in thread with text once the
// question is posted, and returns ask's handle and stdout.
func askAndAnswer(t *testing.T, h *harness, posts int, thread, text string, args ...string) (string, string) {
	t.Helper()
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() { done <- runWith(h, &out, &errb, append([]string{"ask", "--timeout", "20s"}, args...)...) }()
	h.waitPosts(posts)
	if thread == "" {
		thread = h.fake.LastTS()
	}
	h.fake.Reply(t, slacktest.DMChannel, thread, owner, text)
	select {
	case code := <-done:
		require.Equal(t, 0, code, errb.String())
	case <-time.After(20 * time.Second):
		t.Fatal("ask did not return")
	}
	m := regexp.MustCompile(`hikyaku: handle (hk-[a-z2-9]{12})`).FindStringSubmatch(errb.String())
	require.NotNil(t, m, errb.String())
	return m[1], out.String()
}

// AC14: --thread posts a follow-up in the first message's thread, and each
// reply goes to the latest of our messages before it.
func TestAConversationStaysInOneThread(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · main · testhost"
	h.serve()

	first, out := askAndAnswer(t, h, 1, "", "staging, please", "deploy where?")
	assert.Contains(t, out, "\nstaging, please\n")
	root := h.fake.Posts()[0]
	assert.Empty(t, root.ThreadTS)
	assert.Equal(t, []string{h.origin, "Reply in this thread to answer."}, footer(root))
	rootTS := h.fake.LastTS() // the posted question's ts; a reply does not change it

	second, out := askAndAnswer(t, h, 2, rootTS, "yes", "--thread", first, "staging is green. production too?")
	assert.NotEqual(t, first, second)
	follow := h.fake.Posts()[1]
	assert.Equal(t, slacktest.DMChannel, follow.Channel)
	assert.Equal(t, rootTS, follow.ThreadTS, "the follow-up is in the first message's thread")
	assert.Nil(t, footer(follow), "the thread's first message already says who asked and where to reply")
	assert.Contains(t, out, "\nyes\n", "the follow-up hears the reply after it")
	assert.NotContains(t, out, "staging, please", "not the answer to the first question")

	code, out, _ := h.run("", "inbox", first)
	require.Equal(t, 0, code)
	assert.Contains(t, out, "staging, please")
	assert.NotContains(t, out, "\nyes\n", "a reply after the follow-up is not the first question's")

	code, _, _ = h.run("", "send", "--thread", first, "--from", "deploy bot", "done")
	require.Equal(t, 0, code)
	assert.Equal(t, []string{"deploy bot"}, footer(h.waitPosts(3)[2]), "an explicit --from is still shown")

	code, _, errOut := h.run("", "send", "--thread", first, "--channel", "C00000009", "x")
	assert.Equal(t, cli.ExitError, code)
	assert.Contains(t, errOut, "not both")
	code, _, errOut = h.run("", "send", "--thread", "hk-nosuchhandle", "x")
	assert.Equal(t, cli.ExitError, code)
	assert.Contains(t, errOut, "unknown or expired handle")
	assert.Len(t, h.fake.Posts(), 3, "neither was posted")
}
