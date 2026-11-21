package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func host(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

// AC11: the origin names the repository, not the worktree's directory, with
// the branch and the short host name; it names no path, and no agent
// (spec 008, R9).
func TestOriginNamesTheRepositoryNotTheWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "myproject")
	require.NoError(t, os.Mkdir(repo, 0o700))
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	wt := filepath.Join(root, "some-worktree-dir")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat/thing", wt)
	sub := filepath.Join(wt, "internal")
	require.NoError(t, os.Mkdir(sub, 0o700))

	ctx := context.Background()
	t.Setenv("CLAUDECODE", "1")
	assert.Equal(t, "myproject · main · box", detectOrigin(ctx, repo, host("box.example.invalid")),
		"the agent is not named")
	assert.Equal(t, "myproject · feat/thing · box",
		detectOrigin(ctx, sub, host("box")), "a worktree, from a directory inside it")

	plain := filepath.Join(root, "notes")
	require.NoError(t, os.Mkdir(plain, 0o700))
	assert.Equal(t, "notes", detectOrigin(ctx, plain, func() (string, error) { return "", errors.New("no host") }),
		"outside a repository, the directory's name alone")
}
