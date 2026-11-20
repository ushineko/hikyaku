package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func open(t *testing.T, path string, c *clock) *store.Store {
	t.Helper()
	s, err := store.Open(path, 24*time.Hour, c.now)
	require.NoError(t, err)
	return s
}

func TestRepliesAreRecordedOnceAndInOrder(t *testing.T) {
	c := &clock{time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)}
	s := open(t, filepath.Join(t.TempDir(), "messages.json"), c)
	h, err := s.Add("D00000001", "1700000000.000001", "")
	require.NoError(t, err)
	assert.Regexp(t, `^hk-[a-z2-9]{12}$`, h)

	r1 := store.Reply{User: "U00000001", Text: "first", TS: "1700000000.000002"}
	r2 := store.Reply{User: "U00000001", Text: "second", TS: "1700000000.000003"}
	for _, r := range []store.Reply{r1, r2, r1} {
		got, ok := s.Find("D00000001", "1700000000.000001", r.TS)
		require.True(t, ok)
		assert.Equal(t, h, got)
		require.NoError(t, s.AddReply(got, r))
	}
	m, err := s.Get(h)
	require.NoError(t, err)
	assert.Equal(t, []store.Reply{r1, r2}, m.Replies, "a redelivered reply is not recorded twice")

	_, ok := s.Find("D00000001", "1700000000.000009", "1700000000.000010")
	assert.False(t, ok, "a thread that is not ours is not found")
	assert.ErrorIs(t, s.AddReply("hk-nosuchhandle", r1), store.ErrUnknownHandle)
}

// R15: in a thread holding several of our messages, a reply answers the
// latest one posted before it.
func TestAReplyAnswersTheLatestMessageBeforeIt(t *testing.T) {
	c := &clock{time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)}
	s := open(t, filepath.Join(t.TempDir(), "messages.json"), c)
	first, err := s.Add("D00000001", "1700000000.000010", "")
	require.NoError(t, err)
	second, err := s.Add("D00000001", "1700000000.000020", "1700000000.000010")
	require.NoError(t, err)

	for _, tc := range []struct{ reply, want string }{
		{"1700000000.000015", first},  // between the two
		{"1700000000.000025", second}, // after the follow-up
		{"1700000001.000001", second}, // a second later
	} {
		got, ok := s.Find("D00000001", "1700000000.000010", tc.reply)
		require.True(t, ok)
		assert.Equal(t, tc.want, got, "reply at %s", tc.reply)
	}
	_, ok := s.Find("D00000001", "1700000000.000010", "1700000000.000005")
	assert.False(t, ok, "nothing of ours came before it")

	m, err := s.Get(second)
	require.NoError(t, err)
	assert.Equal(t, "1700000000.000010", m.Root())
}

// R8: a handle and its replies survive reopening the store.
func TestTheStoreSurvivesARestart(t *testing.T) {
	c := &clock{time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "messages.json")
	s := open(t, path, c)
	h, err := s.Add("C00000001", "1700000000.000002", "")
	require.NoError(t, err)
	require.NoError(t, s.AddReply(h, store.Reply{User: "U00000001", Text: "yes", TS: "1700000000.000003"}))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	m, err := open(t, path, c).Get(h)
	require.NoError(t, err)
	require.Len(t, m.Replies, 1)
	assert.Equal(t, "yes", m.Replies[0].Text)
}

func TestHandlesExpire(t *testing.T) {
	c := &clock{time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "messages.json")
	s := open(t, path, c)
	h, err := s.Add("D00000001", "1700000000.000003", "")
	require.NoError(t, err)

	c.t = c.t.Add(25 * time.Hour)
	_, err = s.Get(h)
	require.ErrorIs(t, err, store.ErrUnknownHandle)
	_, err = open(t, path, c).Get(h)
	require.ErrorIs(t, err, store.ErrUnknownHandle, "and stays expired after a restart")
}

func TestACorruptStoreIsAnErrorNotAnEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	_, err := store.Open(path, time.Hour, nil)
	assert.Error(t, err)
}
