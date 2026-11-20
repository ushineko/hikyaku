package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agents maps an environment variable an agent sets to the agent's name.
var agents = []struct{ env, value, name string }{ //nolint:gochecknoglobals // a fixed table
	{"CLAUDECODE", "1", "Claude Code"},
}

// detectOrigin is the line under a message that says where it came from:
// the project (the git repository's name, else the directory's), the branch,
// the short host name, and the agent when the environment names one. It
// carries no path: a path names the user and the layout of their disk.
func detectOrigin(ctx context.Context, dir string, getenv func(string) string, hostname func() (string, error)) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var parts []string
	project, branch := gitOrigin(ctx, dir)
	if project == "" {
		project = filepath.Base(dir)
	}
	parts = append(parts, project)
	if branch != "" {
		parts = append(parts, branch)
	}
	if h, err := hostname(); err == nil && h != "" {
		parts = append(parts, strings.SplitN(h, ".", 2)[0])
	}
	for _, a := range agents {
		if getenv(a.env) == a.value {
			parts = append(parts, a.name)
			break
		}
	}
	return strings.Join(parts, " · ")
}

// gitOrigin returns the repository's name and the current branch, or two
// empty strings outside a repository or without git. The name comes from the
// common git directory, so a linked worktree reports its repository and not
// its own directory.
func gitOrigin(ctx context.Context, dir string) (project, branch string) {
	common, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil || common == "" {
		return "", ""
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) == ".git" {
		project = filepath.Base(filepath.Dir(common))
	} else {
		project = strings.TrimSuffix(filepath.Base(common), ".git")
	}
	branch, _ = git(ctx, dir, "branch", "--show-current")
	return project, branch
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	// The program and its arguments are fixed by the callers above; dir is the
	// process's own working directory, passed as one argument with no shell.
	// A repository's config can name programs for git to run; the two
	// commands used do not run them, and fsmonitor is turned off regardless.
	base := []string{"-C", dir, "-c", "core.fsmonitor=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...) //nolint:gosec // see above
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err //nolint:wrapcheck // the caller treats any failure as "not a repository"
	}
	return strings.TrimSpace(string(out)), nil
}

// processOrigin is detectOrigin for this process: its directory, environment
// and host.
func processOrigin(ctx context.Context) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return detectOrigin(ctx, dir, os.Getenv, os.Hostname)
}
