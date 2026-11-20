package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/config"
	"github.com/ushineko/hikyaku/internal/secret"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestMissingFileIsEmptySettings(t *testing.T) {
	s, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	require.NoError(t, err)
	assert.Empty(t, s.Repliers())
	assert.Equal(t, config.DefaultRetention, s.RetentionPeriod())
	assert.IsType(t, secret.Keyring{}, s.Secrets(t.TempDir()), "the keyring is the default store")
}

// R6: an empty allow-list means the owner alone; a list replaces the owner.
func TestRepliersDefaultToTheOwner(t *testing.T) {
	s, err := config.Load(write(t, "owner: U00000001\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"U00000001"}, s.Repliers())

	s, err = config.Load(write(t, "owner: U00000001\nallowed_repliers: [U00000002, U00000003]\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"U00000002", "U00000003"}, s.Repliers())
}

func TestFileStoreOnlyWhenNamed(t *testing.T) {
	s, err := config.Load(write(t, "token_store: file\nretention: 48h\n"))
	require.NoError(t, err)
	dir := t.TempDir()
	assert.Equal(t, secret.File{Dir: dir}, s.Secrets(dir))
	assert.Equal(t, 48*time.Hour, s.RetentionPeriod())
}

func TestInvalidSettingsAreRefused(t *testing.T) {
	for _, body := range []string{"token_store: plaintext\n", "retention: soon\n", "retention: -1h\n", "owner: [\n"} {
		_, err := config.Load(write(t, body))
		assert.Error(t, err, body)
	}
}
