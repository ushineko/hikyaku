package secret_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/ushineko/hikyaku/internal/secret"
)

const (
	botToken = "xoxb-not-a-real-token-1111"
	appToken = "xapp-not-a-real-token-2222"
)

func TestCheckTrimsAndChecksThePrefix(t *testing.T) {
	v, err := secret.Check(secret.Bot, "  "+botToken+"\n")
	require.NoError(t, err)
	assert.Equal(t, botToken, v)

	_, err = secret.Check(secret.Bot, appToken)
	require.ErrorIs(t, err, secret.ErrBadPrefix)
	assert.NotContains(t, err.Error(), appToken, "the refusal must not repeat the value")

	_, err = secret.Check(secret.App, "xapp-")
	require.ErrorIs(t, err, secret.ErrBadPrefix, "a bare prefix is not a token")
}

// Each store keeps a token, reports a missing one as ErrNotFound, and
// forgets one on Delete. The keyring is go-keyring's in-memory mock, never
// the user's own Secret Service.
func TestStores(t *testing.T) {
	keyring.MockInit()
	dir := filepath.Join(t.TempDir(), "conf")
	stores := map[string]secret.Store{"keyring": secret.Keyring{}, "file": secret.File{Dir: dir}}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			_, err := s.Get(secret.Bot)
			require.ErrorIs(t, err, secret.ErrNotFound)

			require.NoError(t, s.Set(secret.Bot, botToken))
			got, err := s.Get(secret.Bot)
			require.NoError(t, err)
			assert.Equal(t, botToken, got)

			require.NoError(t, s.Delete(secret.Bot))
			_, err = s.Get(secret.Bot)
			require.ErrorIs(t, err, secret.ErrNotFound)
			require.NoError(t, s.Delete(secret.Bot), "deleting a missing token is not an error")
		})
	}
}

func TestFileStoreIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "conf")
	require.NoError(t, secret.File{Dir: dir}.Set(secret.App, appToken))
	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	fi, err := os.Stat(filepath.Join(dir, "app.token"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestRedactorRemovesKnownValuesAndTokenShapes(t *testing.T) {
	r := &secret.Redactor{}
	r.Add(botToken)
	r.Add("")
	out := r.String("bot " + botToken + " and a stranger xoxp-123-456-abc and xapp-1-A0-xyz")
	assert.NotContains(t, out, botToken)
	assert.NotContains(t, out, "xoxp-123")
	assert.NotContains(t, out, "xapp-1-A0")
	assert.Contains(t, out, "[redacted]")
}

// Every way a token can reach a log line: the message, a string attribute,
// an error, a stringer, a group and an attribute bound with With.
func TestLogHandlerRedactsEveryAttributeKind(t *testing.T) {
	var buf bytes.Buffer
	r := &secret.Redactor{}
	r.Add(botToken)
	r.Add(appToken)
	log := slog.New(r.Handler(slog.NewTextHandler(&buf, nil))).With("bound", appToken)
	log.Info("message "+botToken,
		"s", botToken,
		"err", fmt.Errorf("wrapped: %w", errors.New(appToken)),
		"stringer", stringer(botToken),
		slog.Group("g", "inner", appToken),
	)
	out := buf.String()
	assert.NotContains(t, out, botToken)
	assert.NotContains(t, out, appToken)
	assert.Contains(t, out, "[redacted]")
}

type stringer string

func (s stringer) String() string { return string(s) }
