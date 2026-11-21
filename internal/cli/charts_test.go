package cli_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/slacktest"
)

// fakeCharter stands in for the boxed renderer: it records what it was
// given and returns a PNG, or fails for a diagram containing "fail".
type fakeCharter struct {
	mu   sync.Mutex
	srcs []string
}

func (f *fakeCharter) Render(_ context.Context, src string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.srcs = append(f.srcs, src)
	if strings.Contains(src, "fail") {
		return nil, errors.New("the renderer failed")
	}
	return []byte("\x89PNG\r\n\x1a\n" + src), nil
}

func (f *fakeCharter) given() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.srcs...)
}

func chartHarness(t *testing.T, answer string) (*harness, *fakeCharter) {
	t.Helper()
	h := newHarness(t)
	c := &fakeCharter{}
	h.charts = c
	h.runner = &fakeRunner{answer: answers(answer, 0.1)}
	h.serve()
	switchAnswerer(t, h, "on")
	return h, c
}

// AC3: a diagram is drawn from the scrubbed answer, the text says it is
// attached, and the image follows in the same thread; one that does not
// render stays as code.
func TestAChartIsAttachedToItsAnswer(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	key := "glpat-" + strings.Repeat("Q7", 12)
	h, c := chartHarness(t, "Spend this week:\n```mermaid\npie\n  \"Mon\": 1\n  \""+home+"/git/x/y.go "+key+"\": 2\n```\n"+
		"And the broken one:\n```mermaid\nflowchart LR\n  fail-->B\n```\nDone.")

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "chart my spend")
	got := h.postIn(ts, startsWith("Spend this week:"))
	assert.Equal(t, "Spend this week:\n_(chart attached)_\nAnd the broken one:\n```\nflowchart LR\n  fail-->B\n```\nDone."+
		"\n\n_Something that looked like a secret was removed from this answer._", got.Text)

	require.Eventually(t, func() bool { return len(h.fake.Uploads()) == 1 }, 5*time.Second, 10*time.Millisecond)
	up := h.fake.Uploads()[0]
	assert.Equal(t, slacktest.DMChannel, up.Channel)
	assert.Equal(t, ts, up.ThreadTS, "in the question's thread, after the answer")
	assert.Equal(t, "Chart", up.Title)
	assert.True(t, strings.HasPrefix(string(up.Data), "\x89PNG"))

	drawn := c.given()[0]
	assert.NotContains(t, drawn, key, "the renderer never sees a secret")
	assert.NotContains(t, drawn, home, "or a path from the machine")
	assert.Contains(t, drawn, "x/y.go [redacted]")
}

// AC4: without files:write, the first chart is posted as its source with a
// note that it could not be attached, the log says what is missing once,
// and later answers keep their diagrams as code instead of promising one.
func TestWithoutTheFilesScopeAChartIsPostedAsCode(t *testing.T) {
	h, _ := chartHarness(t, "A:\n```mermaid\npie\n  \"a\": 1\n```")
	h.fake.NoFileScope()
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "one")
	h.postIn(ts, is("A:\n_(chart attached)_"))
	h.postIn(ts, is("_The chart could not be attached; here is its source._\n```\npie\n  \"a\": 1\n```"))

	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "two")
	h.postIn(ts, is("A:\n```mermaid\npie\n  \"a\": 1\n```"))
	time.Sleep(200 * time.Millisecond)
	for _, p := range h.fake.Posts() {
		if p.ThreadTS == ts {
			assert.NotContains(t, p.Text, "chart attached", "no attachment is promised once uploads are known to fail")
		}
	}
	assert.Empty(t, h.fake.Uploads())
	assert.Equal(t, 1, strings.Count(h.serveLog.String(), "no files:write scope"))
}

// R3: with no renderer, a diagram stays as the code the run wrote.
func TestWithoutARendererADiagramStaysAsCode(t *testing.T) {
	h := newHarness(t)
	h.runner = &fakeRunner{answer: answers("A:\n```mermaid\npie\n  \"a\": 1\n```", 0.1)}
	h.serve()
	switchAnswerer(t, h, "on")
	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "chart")
	h.postIn(ts, is("A:\n```mermaid\npie\n  \"a\": 1\n```"))
	assert.Empty(t, h.fake.Uploads())
}
