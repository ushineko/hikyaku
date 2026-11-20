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

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// AC11: the origin names the repository, not the worktree's directory, with
// the branch, the short host name and the agent; it names no path.
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
	assert.Equal(t, "myproject · main · box · Claude Code",
		detectOrigin(ctx, repo, env(map[string]string{"CLAUDECODE": "1"}), host("box.example.invalid")))
	assert.Equal(t, "myproject · feat/thing · box",
		detectOrigin(ctx, sub, env(nil), host("box")), "a worktree, from a directory inside it")

	plain := filepath.Join(root, "notes")
	require.NoError(t, os.Mkdir(plain, 0o700))
	assert.Equal(t, "notes", detectOrigin(ctx, plain, env(nil), func() (string, error) { return "", errors.New("no host") }),
		"outside a repository, the directory's name alone")
}
