/*
Package chart renders the Mermaid diagrams in an answer to PNG images
(spec 011), inside a bubblewrap box that has no network, no view of the
owner's home directory or /run, and one writable folder (threat model S8).

The renderer is a browser engine running a JavaScript library over text a
model wrote, which a source the run read may have steered, so the box,
Mermaid's strict mode and the limits here are the security contract.
*/
package chart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limits (S8).
const (
	MaxInput  = 20 << 10
	MaxOutput = 5 << 20
	MaxCharts = 2
	// Timeout is how long one render may take.
	Timeout = 30 * time.Second
)

// config is Mermaid's configuration: strict mode drops HTML in labels and
// click callbacks.
const config = `{"securityLevel":"strict","theme":"default"}`

var (
	block = regexp.MustCompile("(?s)```mermaid[ \\t]*\\n(.*?)\\n?```")
	// directive matches %%{init: …}%% and the like, which could change the
	// configuration from inside the diagram.
	directive = regexp.MustCompile(`(?s)%%\{.*?\}%%`)
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
)

// ErrTooBig is returned for a diagram over MaxInput or an image over
// MaxOutput.
var ErrTooBig = errors.New("too big")

// Placeholder marks where chart i was taken out of a text by Extract.
func Placeholder(i int) string { return "chart" + strconv.Itoa(i) + "" }

// Extract takes the first MaxCharts mermaid blocks out of text, each
// replaced by Placeholder(i), and returns their sources, with directives
// removed. Blocks after those stay in the text as they are.
func Extract(text string) (string, []string) {
	var sources []string
	out := block.ReplaceAllStringFunc(text, func(m string) string {
		if len(sources) >= MaxCharts {
			return m
		}
		src := strings.TrimSpace(directive.ReplaceAllString(block.FindStringSubmatch(m)[1], ""))
		sources = append(sources, src)
		return Placeholder(len(sources) - 1)
	})
	return out, sources
}

// AsCode is a diagram's source as a code block, for one that was not
// rendered.
func AsCode(src string) string { return "```\n" + src + "\n```" }

// Renderer renders inside the box. Bwrap, Node and Mmdc are the commands;
// Home is the directory masked from the renderer.
type Renderer struct {
	Bwrap, Node, Mmdc string
	Home              string
	// TempDir is where each render's folder is made; empty is the system's.
	TempDir string
	// Timeout is a render's limit; zero is Timeout.
	Timeout time.Duration
}

// Find is a Renderer from the system's bubblewrap, Node and mmdc, or the
// reason there is none.
func Find(home string) (*Renderer, string) {
	r := &Renderer{Home: home}
	var err error
	if r.Bwrap, err = exec.LookPath("bwrap"); err != nil {
		return nil, "bubblewrap (bwrap) is not installed"
	}
	if r.Mmdc, err = exec.LookPath("mmdc"); err != nil {
		return nil, "mermaid-cli (mmdc) is not installed"
	}
	// The system's Node, not one from the owner's home, which the box hides.
	for _, n := range []string{"/usr/bin/node", "/usr/local/bin/node"} {
		if _, err := os.Stat(n); err == nil {
			r.Node = n
			return r, ""
		}
	}
	return nil, "no system Node.js (/usr/bin/node) for mmdc"
}

// Args are the box and the render, for a working folder dir.
func (r *Renderer) Args(dir string) []string {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
	}
	if r.Home != "" && r.Home != "/" {
		args = append(args, "--tmpfs", r.Home)
	}
	return append(args,
		"--dir", "/tmp/work",
		"--bind", dir, "/tmp/work",
		"--unshare-all",
		"--die-with-parent",
		"--new-session",
		"--clearenv",
		"--setenv", "PATH", "/usr/bin:/bin",
		"--setenv", "HOME", "/tmp",
		"--chdir", "/tmp/work",
		r.Node, r.Mmdc,
		"-i", "in.mmd", "-o", "out.png", "-c", "config.json",
		"-b", "white", "-s", "2", "-q",
	)
}

// Render renders one diagram's source to a PNG.
func (r *Renderer) Render(ctx context.Context, src string) ([]byte, error) {
	src = strings.TrimSpace(directive.ReplaceAllString(src, ""))
	if len(src) > MaxInput {
		return nil, fmt.Errorf("diagram of %d bytes: %w", len(src), ErrTooBig)
	}
	dir, err := os.MkdirTemp(r.TempDir, "hikyaku-chart-")
	if err != nil {
		return nil, fmt.Errorf("make the render's folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "in.mmd"), []byte(src+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write the diagram: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
		return nil, fmt.Errorf("write the config: %w", err)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Bwrap, r.Args(dir)...) //nolint:gosec // the commands are found on the system, the input is a file
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, n: 4 << 10}
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("render: %w", ctx.Err())
	}
	if runErr != nil {
		return nil, fmt.Errorf("render: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	out := filepath.Join(dir, "out.png")
	fi, err := os.Stat(out)
	if err != nil {
		return nil, fmt.Errorf("render made no image: %w", err)
	}
	if fi.Size() > MaxOutput {
		return nil, fmt.Errorf("image of %d bytes: %w", fi.Size(), ErrTooBig)
	}
	png, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("read the image: %w", err)
	}
	if !bytes.HasPrefix(png, pngMagic) {
		return nil, errors.New("render made something that is not a PNG")
	}
	return png, nil
}

type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
