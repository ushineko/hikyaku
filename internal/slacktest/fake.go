/*
Package slacktest is a fake Slack for tests: the Web API methods hikyaku
calls, and a Socket Mode websocket that can deliver thread replies and drop
the connection on cue.

Every ID it hands out is invented (T00000000, D00000001); nothing here comes
from a real workspace.
*/
package slacktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// DMChannel is the ID conversations.open returns.
const DMChannel = "D00000001"

// Post is one chat.postMessage call as the fake received it.
type Post struct {
	Channel  string
	Text     string
	ThreadTS string
	Blocks   []map[string]any
}

// Fake is the fake Slack.
type Fake struct {
	srv      *httptest.Server
	botToken string
	appToken string

	mu sync.Mutex
	// Posts is every chat.postMessage, in order.
	posts []Post
	// opened is every user conversations.open was asked about.
	opened []string
	// conns are the open Socket Mode connections.
	conns []*websocket.Conn
	// connects counts apps.connections.open calls.
	connects int
	seq      int
	lastTS   string
	// PostError, when set, is the error chat.postMessage answers with.
	postError string
	// rejectApp makes apps.connections.open answer invalid_auth.
	rejectApp bool
	connected chan struct{}
}

// New starts a fake Slack that accepts the two tokens given.
func New(t *testing.T, botToken, appToken string) *Fake {
	t.Helper()
	f := &Fake{botToken: botToken, appToken: appToken, connected: make(chan struct{}, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps.connections.open", f.connectionsOpen)
	mux.HandleFunc("/api/chat.postMessage", f.postMessage)
	mux.HandleFunc("/api/conversations.open", f.conversationsOpen)
	mux.HandleFunc("/ws", f.websocket)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// APIURL is the address to give slack-go, ending in a slash.
func (f *Fake) APIURL() string { return f.srv.URL + "/api/" }

// Close drops every connection and stops the server.
func (f *Fake) Close() {
	f.Drop()
	f.srv.CloseClientConnections()
	f.srv.Close()
}

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	_ = r.ParseForm()
	return r.Form.Get("token")
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Fake) connectionsOpen(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	reject := f.rejectApp
	f.connects++
	f.mu.Unlock()
	if reject || bearer(r) != f.appToken {
		reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	reply(w, map[string]any{"ok": true, "url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws"})
}

func (f *Fake) conversationsOpen(w http.ResponseWriter, r *http.Request) {
	if bearer(r) != f.botToken {
		reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	f.opened = append(f.opened, r.Form.Get("users"))
	f.mu.Unlock()
	reply(w, map[string]any{"ok": true, "channel": map[string]any{"id": DMChannel}})
}

func (f *Fake) postMessage(w http.ResponseWriter, r *http.Request) {
	if bearer(r) != f.botToken {
		reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	_ = r.ParseForm()
	p := Post{Channel: r.Form.Get("channel"), Text: r.Form.Get("text"), ThreadTS: r.Form.Get("thread_ts")}
	if b := r.Form.Get("blocks"); b != "" {
		_ = json.Unmarshal([]byte(b), &p.Blocks)
	}
	f.mu.Lock()
	if f.postError != "" {
		e := f.postError
		f.mu.Unlock()
		reply(w, map[string]any{"ok": false, "error": e})
		return
	}
	f.seq++
	ts := fmt.Sprintf("1700000000.%06d", f.seq)
	f.posts = append(f.posts, p)
	f.lastTS = ts
	f.mu.Unlock()
	reply(w, map[string]any{"ok": true, "channel": p.Channel, "ts": ts})
}

func (f *Fake) websocket(w http.ResponseWriter, r *http.Request) {
	// slack-go sends an Origin header the default check refuses; a fake on
	// loopback has no origin to protect.
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	hello := map[string]any{
		"type": "hello", "num_connections": 1,
		"connection_info": map[string]any{"app_id": "A00000000"},
		"debug_info":      map[string]any{"host": "slacktest"},
	}
	if err := conn.WriteJSON(hello); err != nil {
		_ = conn.Close()
		return
	}
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
	select {
	case f.connected <- struct{}{}:
	default:
	}
	// Read acks until the connection goes; Slack sends pings, and slack-go
	// treats a quiet connection as dead, so the fake pings too.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			f.mu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
			f.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// WaitConnected blocks until a Socket Mode connection is made, or fails the
// test after a timeout.
func (f *Fake) WaitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-f.connected:
	case <-time.After(10 * time.Second):
		t.Fatal("no socket mode connection")
	}
}

// Drop closes every Socket Mode connection, as a network failure would.
func (f *Fake) Drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

// Reply delivers a message from user in the thread channel/threadTS of a
// DM over every open Socket Mode connection, and returns its ts.
func (f *Fake) Reply(t *testing.T, channel, threadTS, user, text string) string {
	t.Helper()
	return f.deliver(t, map[string]any{"channel": channel, "user": user, "text": text, "thread_ts": threadTS, "channel_type": "im"})
}

// Say delivers a top-level message from user in channel, whose type is
// "im", "channel" or "group", and returns its ts.
func (f *Fake) Say(t *testing.T, channel, channelType, user, text string) string {
	t.Helper()
	return f.deliver(t, map[string]any{"channel": channel, "user": user, "text": text, "channel_type": channelType})
}

// Bot delivers a top-level message from a bot in the DM channel.
func (f *Fake) Bot(t *testing.T, channel, text string) {
	t.Helper()
	f.deliver(t, map[string]any{"channel": channel, "bot_id": "B00000001", "text": text, "channel_type": "im"})
}

func (f *Fake) deliver(t *testing.T, event map[string]any) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	ts := fmt.Sprintf("1700000000.%06d", f.seq)
	event["type"] = "message"
	event["ts"] = ts
	env := map[string]any{
		"envelope_id": fmt.Sprintf("env-%d", f.seq), "type": "events_api", "accepts_response_payload": false,
		"payload": map[string]any{
			"token": "invented", "team_id": "T00000000", "api_app_id": "A00000000", "type": "event_callback",
			"event_id": fmt.Sprintf("Ev%d", f.seq), "event_time": 1700000001,
			"event": event,
		},
	}
	if len(f.conns) == 0 {
		t.Fatal("message with no socket mode connection")
	}
	for _, c := range f.conns {
		if err := c.WriteJSON(env); err != nil {
			t.Fatalf("deliver message: %v", err)
		}
	}
	return ts
}

// Posts returns every chat.postMessage so far.
func (f *Fake) Posts() []Post {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Post(nil), f.posts...)
}

// LastTS is the ts of the most recent post.
func (f *Fake) LastTS() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTS
}

// Opened returns the users conversations.open was asked about.
func (f *Fake) Opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

// Connects counts apps.connections.open calls.
func (f *Fake) Connects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

// FailPosts makes chat.postMessage answer with err until called with "".
func (f *Fake) FailPosts(err string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postError = err
}

// RejectAppToken makes apps.connections.open answer invalid_auth.
func (f *Fake) RejectAppToken() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectApp = true
}
