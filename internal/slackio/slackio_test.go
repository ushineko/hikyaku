package slackio_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/slackio"
	"github.com/ushineko/hikyaku/internal/slacktest"
)

const (
	botToken = "xoxb-not-a-real-token-1111"
	appToken = "xapp-not-a-real-token-2222"
)

func blocksJSON(t *testing.T, m slackio.Outgoing) (string, []map[string]any) {
	t.Helper()
	fallback, blocks := slackio.Compose(m)
	b, err := json.Marshal(blocks)
	require.NoError(t, err)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return fallback, out
}

func TestAPlainMessageIsTextAlone(t *testing.T) {
	fallback, blocks := slackio.Compose(slackio.Outgoing{Text: "build *done* <https://example.invalid|log>"})
	assert.Equal(t, "build *done* <https://example.invalid|log>", fallback)
	assert.Nil(t, blocks)
}

func TestATitleAndStatusMakeAHeader(t *testing.T) {
	fallback, blocks := blocksJSON(t, slackio.Outgoing{Text: "all green", Title: "build", Status: slackio.StatusOK})
	require.Len(t, blocks, 2)
	assert.Equal(t, "header", blocks[0]["type"])
	assert.Equal(t, ":white_check_mark: build", blocks[0]["text"].(map[string]any)["text"])
	assert.Equal(t, "section", blocks[1]["type"])
	assert.Equal(t, "mrkdwn", blocks[1]["text"].(map[string]any)["type"])
	assert.Equal(t, "build: all green", fallback, "the notification still says what happened")
}

func TestALongBodyIsSplitAcrossSections(t *testing.T) {
	body := strings.Repeat("line of output é\n", 400) // ~7 KB, with multibyte runes
	_, blocks := blocksJSON(t, slackio.Outgoing{Text: body, Status: slackio.StatusFail})
	var joined strings.Builder
	for _, b := range blocks[1:] {
		text := b["text"].(map[string]any)["text"].(string)
		assert.LessOrEqual(t, len(text), 3000)
		joined.WriteString(text)
	}
	assert.Equal(t, body, joined.String(), "nothing is lost or broken at a cut")
}

func TestParseStatus(t *testing.T) {
	for _, s := range []string{"", "ok", "warn", "fail"} {
		_, err := slackio.ParseStatus(s)
		assert.NoError(t, err, s)
	}
	_, err := slackio.ParseStatus("green")
	assert.Error(t, err)
}

func listen(t *testing.T, f *slacktest.Fake) (<-chan slackio.Incoming, *atomic.Bool, <-chan error, context.CancelFunc) {
	t.Helper()
	c := slackio.New(slackio.Options{BotToken: botToken, AppToken: appToken, APIURL: f.APIURL()})
	in := make(chan slackio.Incoming, 4)
	var up atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.Listen(ctx, in, up.Store) }()
	t.Cleanup(cancel)
	return in, &up, errc, cancel
}

// AC1: the service connects, and after Slack drops the connection it
// connects again.
func TestTheConnectionComesBackAfterADrop(t *testing.T) {
	f := slacktest.New(t, botToken, appToken)
	in, up, _, _ := listen(t, f)
	f.WaitConnected(t)
	assert.Eventually(t, up.Load, 5*time.Second, 10*time.Millisecond)

	f.Drop()
	f.WaitConnected(t)
	assert.GreaterOrEqual(t, f.Connects(), 2)

	f.Reply(t, "D00000001", "1699999999.000001", "U00000001", "still here")
	select {
	case m := <-in:
		assert.Equal(t, "still here", m.Text)
	case <-time.After(5 * time.Second):
		t.Fatal("no reply over the new connection")
	}
}

func TestRefusedTokensStopTheListener(t *testing.T) {
	f := slacktest.New(t, botToken, appToken)
	f.RejectAppToken()
	_, _, errc, _ := listen(t, f)
	select {
	case err := <-errc:
		require.ErrorIs(t, err, slackio.ErrAuth)
		assert.NotContains(t, err.Error(), appToken)
	case <-time.After(10 * time.Second):
		t.Fatal("the listener kept retrying a refused token")
	}
}

// Only a person's new message inside a thread is a reply.
func TestOnlyThreadRepliesAreReported(t *testing.T) {
	f := slacktest.New(t, botToken, appToken)
	in, _, _, _ := listen(t, f)
	f.WaitConnected(t)

	f.Reply(t, "D00000001", "", "U00000001", "top level, not a reply")
	f.Reply(t, "D00000001", "1700000000.000009", "U00000001", "a reply")
	select {
	case m := <-in:
		assert.Equal(t, "a reply", m.Text)
		assert.Equal(t, "1700000000.000009", m.ThreadTS)
		assert.Equal(t, "U00000001", m.User)
	case <-time.After(5 * time.Second):
		t.Fatal("no reply reported")
	}
	select {
	case m := <-in:
		t.Fatalf("unexpected message reported: %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// The origin is one short line however it arrives, and a question adds the
// reply hint after it.
func TestTheFooterIsOneShortLine(t *testing.T) {
	from := "nightly\nbuild   " + strings.Repeat("é", 300)
	fallback, blocks := blocksJSON(t, slackio.Outgoing{Text: "done", From: from, Question: true})
	assert.Equal(t, "done", fallback)
	require.Len(t, blocks, 2)
	assert.Equal(t, "context", blocks[1]["type"])
	els := blocks[1]["elements"].([]any)
	require.Len(t, els, 1, "one element, so a copied message reads as one line")
	got := els[0].(map[string]any)["text"].(string)
	from, hint, ok := strings.Cut(got, " · "+slackio.ReplyHint)
	require.True(t, ok, got)
	assert.Empty(t, hint)
	assert.True(t, strings.HasPrefix(from, "nightly build éé"), from)
	assert.NotContains(t, got, "\n")
	assert.Equal(t, 200, len([]rune(from)))
}
