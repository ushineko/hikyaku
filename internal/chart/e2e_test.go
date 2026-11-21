//go:build e2e

package chart_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/chart"
)

// AC5, against the real bubblewrap and mermaid-cli: `go test -tags e2e
// ./internal/chart/`.

func realRenderer(t *testing.T) *chart.Renderer {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	r, why := chart.Find(home)
	if r == nil {
		t.Skip(why)
	}
	return r
}

// listener counts connections to a port on this machine's loopback.
func listener(t *testing.T) (int, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	var hits atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			_ = c.Close()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, &hits
}

// A diagram built to reach the network, run script or loosen the security
// level renders (or fails) without anything reaching the machine's network.
func TestAHostileChartReachesNothing(t *testing.T) {
	r := realRenderer(t)
	port, hits := listener(t)
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	src := "%%{init: {\"securityLevel\": \"loose\"}}%%\n" +
		"flowchart LR\n" +
		"  A[\"<img src='" + u + "/img'>\"] --> B[\"<script>fetch('" + u + "/s')</script>\"]\n" +
		"  click A \"" + u + "/click\"\n" +
		"  style B fill:url(" + u + "/css)\n"
	png, err := r.Render(context.Background(), src)
	t.Logf("all-out attempt: png=%d bytes err=%v", len(png), err != nil)

	// One that is valid Mermaid, so it is drawn: HTML in a label and a
	// click link, which strict mode should render as text and drop.
	valid := "flowchart LR\n" +
		"  A[\"<img src='" + u + "/img2'> start\"] --> B[\"<a href='" + u + "/a'>end</a>\"]\n" +
		"  click A \"" + u + "/click2\" \"tip\"\n"
	png, err = r.Render(context.Background(), valid)
	require.NoError(t, err, "a valid diagram renders in the box")
	assert.True(t, len(png) > 1000, "an image was drawn")
	time.Sleep(time.Second)
	assert.Zero(t, hits.Load(), "the renderer reached the machine's network")
}

// The box hides the home directory and /run and has no network: a probe
// run in it instead of the renderer can read none of them.
func TestTheBoxHidesTheHomeAndTheNetwork(t *testing.T) {
	r := realRenderer(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	canary := hex.EncodeToString(b)
	f, err := os.CreateTemp(home, ".hikyaku-e2e-canary-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	_, _ = f.WriteString(canary)
	_ = f.Close()
	port, hits := listener(t)

	dir := t.TempDir()
	probe := "#!/bin/sh\n" +
		"cat " + f.Name() + " > /tmp/work/home 2>&1\n" +
		"ls /run/user > /tmp/work/run 2>&1\n" +
		"(exec 3<>/dev/tcp/127.0.0.1/" + fmt.Sprint(port) + ") > /tmp/work/net 2>&1 || echo refused >> /tmp/work/net\n" +
		"env > /tmp/work/env\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "probe.sh"), []byte(probe), 0o700)) //nolint:gosec // a test script
	boxed := *r
	boxed.Node, boxed.Mmdc = "/bin/bash", "/tmp/work/probe.sh"
	out, err := exec.Command(boxed.Bwrap, boxed.Args(dir)...).CombinedOutput() //nolint:gosec // the test's own box
	require.NoError(t, err, string(out))

	got := func(n string) string { b, _ := os.ReadFile(filepath.Join(dir, n)); return string(b) }
	assert.NotContains(t, got("home"), canary, "the home directory was readable")
	assert.Contains(t, got("run"), "No such file", "/run/user was visible")
	assert.Contains(t, got("net"), "refused")
	assert.Zero(t, hits.Load(), "the box reached the machine's network")
	assert.NotContains(t, got("env"), "HIKYAKU")
	assert.NotContains(t, got("env"), "xox")
	t.Logf("env in the box:\n%s", got("env"))
}
