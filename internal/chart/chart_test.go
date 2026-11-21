package chart_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/chart"
)

// AC1: the first two mermaid blocks are taken out, directives and all;
// other code blocks stay.
func TestExtractTakesOutTheDiagrams(t *testing.T) {
	text := "Here:\n```mermaid\n%%{init: {'securityLevel':'loose'}}%%\nflowchart LR\n  A-->B\n```\nand\n" +
		"```go\nfmt.Println()\n```\n```mermaid\npie\n  \"a\": 1\n```\n```mermaid\nflowchart TD\n  C\n```"
	out, srcs := chart.Extract(text)
	assert.Equal(t, []string{"flowchart LR\n  A-->B", "pie\n  \"a\": 1"}, srcs, "directives are removed")
	assert.Equal(t, "Here:\n"+chart.Placeholder(0)+"\nand\n```go\nfmt.Println()\n```\n"+chart.Placeholder(1)+
		"\n```mermaid\nflowchart TD\n  C\n```", out, "a third diagram stays as it was")

	none, srcs := chart.Extract("no charts here")
	assert.Equal(t, "no charts here", none)
	assert.Empty(t, srcs)
}

// stand-in for bwrap: records its arguments, then does what body says in
// the folder bound to /tmp/work.
func fakeBwrap(t *testing.T, rec, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bwrap")
	script := "#!/bin/sh\n" +
		`for a in "$@"; do printf '%s\n' "$a"; done > "` + rec + `/args"` + "\n" +
		`while [ "$1" != "--bind" ]; do shift; done; work="$2"` + "\n" +
		`cp "$work/in.mmd" "` + rec + `/in.mmd"; cp "$work/config.json" "` + rec + `/config.json"` + "\n" +
		body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700)) //nolint:gosec // an executable test script
	return path
}

func renderer(t *testing.T, bwrap string) *chart.Renderer {
	t.Helper()
	return &chart.Renderer{Bwrap: bwrap, Node: "/usr/bin/node", Mmdc: "/usr/bin/mmdc", Home: "/home/invented",
		TempDir: t.TempDir()}
}

const png = `printf '\211PNG\r\n\032\nrest' > "$work/out.png"`

// AC2: the renderer runs boxed: no namespace shared, nothing inherited, the
// home directory and /run hidden, one writable folder, strict mode, the
// system's Node; and the diagram it gets has no directives.
func TestARenderIsBoxed(t *testing.T) {
	rec := t.TempDir()
	r := renderer(t, fakeBwrap(t, rec, png))
	got, err := r.Render(context.Background(), "%%{init: {\"securityLevel\":\"loose\"}}%%\nflowchart LR\n  A-->B")
	require.NoError(t, err)
	assert.Equal(t, "\x89PNG\r\n\x1a\nrest", string(got))

	args := strings.Split(strings.TrimRight(read(t, rec+"/args"), "\n"), "\n")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind / /", "--tmpfs /tmp", "--tmpfs /run", "--tmpfs /home/invented",
		"--setenv PATH /usr/bin:/bin", "--setenv HOME /tmp", "--chdir /tmp/work",
		"/usr/bin/node /usr/bin/mmdc -i in.mmd -o out.png -c config.json"} {
		assert.Contains(t, joined, want)
	}
	assert.Equal(t, 1, strings.Count(joined, "--bind "), "one writable folder")
	assert.NotContains(t, joined, "--share-net")
	assert.Less(t, strings.Index(joined, "--ro-bind / /"), strings.Index(joined, "--tmpfs /home/invented"),
		"the home is masked after the system is mounted")
	assert.JSONEq(t, `{"securityLevel":"strict","theme":"default"}`, read(t, rec+"/config.json"))
	assert.Equal(t, "flowchart LR\n  A-->B\n", read(t, rec+"/in.mmd"), "no directive reaches the renderer")
	entries, _ := os.ReadDir(r.TempDir)
	assert.Empty(t, entries, "the render's folder is removed")
}

// AC2: limits. Too big in, not a PNG out, too long: refused, and a render
// that runs over is killed with its children.
func TestARenderKeepsToItsLimits(t *testing.T) {
	rec := t.TempDir()
	_, err := renderer(t, fakeBwrap(t, rec, png)).Render(context.Background(), strings.Repeat("x", chart.MaxInput+1))
	require.ErrorIs(t, err, chart.ErrTooBig)

	_, err = renderer(t, fakeBwrap(t, rec, `echo '<svg/>' > "$work/out.png"`)).Render(context.Background(), "pie")
	require.ErrorContains(t, err, "not a PNG")

	big := `head -c ` + "6000000" + ` /dev/zero > "$work/out.png"`
	_, err = renderer(t, fakeBwrap(t, rec, big)).Render(context.Background(), "pie")
	require.ErrorIs(t, err, chart.ErrTooBig)

	r := renderer(t, fakeBwrap(t, rec, `sleep 300 & echo $! > "`+rec+`/child"; wait`))
	r.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err = r.Render(context.Background(), "pie")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	var pid int
	_, _ = fmt.Sscan(read(t, rec+"/child"), &pid)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond,
		"the renderer's child is still alive")
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
