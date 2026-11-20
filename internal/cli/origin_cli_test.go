package cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/slacktest"
)

// footer returns the texts of a post's context block, or nil without one.
func footer(p slacktest.Post) []string {
	for _, b := range p.Blocks {
		if b["type"] != "context" {
			continue
		}
		var texts []string
		for _, e := range b["elements"].([]any) {
			el := e.(map[string]any)
			if el["type"] != "plain_text" {
				texts = append(texts, "NOT PLAIN TEXT: "+el["text"].(string))
				continue
			}
			texts = append(texts, el["text"].(string))
		}
		return texts
	}
	return nil
}

// AC11 and AC12: every message says where it came from, --from replaces or
// removes that, and only a question asks for a reply in its thread.
func TestEveryMessageSaysWhereItCameFrom(t *testing.T) {
	h := newHarness(t)
	h.origin = "hikyaku · feat/x · testhost · Claude Code"
	h.serve()

	code, _, _ := h.run("", "send", "build *done*")
	require.Equal(t, 0, code)
	p := h.waitPosts(1)[0]
	assert.Equal(t, []string{h.origin}, footer(p))
	assert.Equal(t, "build *done*", p.Text, "the notification is the agent's own words")
	assert.Equal(t, "build *done*", p.Blocks[0]["text"].(map[string]any)["text"])

	code, _, _ = h.run("", "send", "--from", "nightly <!channel> bot", "x")
	require.Equal(t, 0, code)
	assert.Equal(t, []string{"nightly <!channel> bot"}, footer(h.waitPosts(2)[1]),
		"--from replaces the origin, as plain text that cannot mention anyone")

	code, _, _ = h.run("", "send", "--from", "", "plain")
	require.Equal(t, 0, code)
	p = h.waitPosts(3)[2]
	assert.Empty(t, p.Blocks, `--from "" leaves a plain message plain`)

	go func() { _, _, _ = h.run("", "ask", "--timeout", "1s", "continue?") }()
	p = h.waitPosts(4)[3]
	assert.Equal(t, []string{h.origin, "Reply in this thread to answer."}, footer(p))
	assert.Equal(t, "continue?", p.Text)
}

// AC13: the handle is on stderr while ask is still waiting, so a question
// whose ask is stopped can still be read with inbox.
func TestAskPrintsTheHandleBeforeTheReply(t *testing.T) {
	h := newHarness(t)
	h.serve()
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() { done <- runWith(h, &out, &errb, "ask", "--timeout", "20s", "deploy?") }()

	require.Eventually(t, func() bool { return strings.Contains(errb.String(), "hikyaku: handle hk-") },
		5*time.Second, 10*time.Millisecond, "no handle while ask waits")
	select {
	case <-done:
		t.Fatal("ask returned before any reply")
	default:
	}
	h.fake.Reply(t, slacktest.DMChannel, h.fake.LastTS(), owner, "yes")
	select {
	case code := <-done:
		assert.Equal(t, 0, code)
	case <-time.After(20 * time.Second):
		t.Fatal("ask did not return")
	}
	assert.Contains(t, out.String(), "\nyes\n")
	assert.Equal(t, 1, strings.Count(errb.String(), "hikyaku: handle"), "the handle is printed once")
}
