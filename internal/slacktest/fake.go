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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// DMChannel is the ID conversations.open returns.
const DMChannel = "D00000001"

// BotUser is the bot's own user ID, as auth.test reports it.
const BotUser = "U0000000B"

// Post is one chat.postMessage call as the fake received it.
type Post struct {
	Channel  string
	Text     string
	ThreadTS string
	Blocks   []map[string]any
	// UnfurlLinks and UnfurlMedia are the flags as sent: "" when not sent.
	UnfurlLinks, UnfurlMedia string
}

// Reaction is one reactions.add or reactions.remove call as the fake
// received it.
type Reaction struct {
	Channel, TS, Name string
	Removed           bool
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
	// reactions is every reactions.add and reactions.remove, in order.
	reactions []Reaction
	// noReactions makes the reactions methods answer missing_scope.
	noReactions bool
	// mes is the text of every chat.meMessage, in order.
	mes []Post
	// joins and leaves are the channels conversations.join and
	// conversations.leave were called for; private are channels join
	// refuses, as Slack does for a private one.
	joins, leaves []string
	private       map[string]bool
	// threads are the messages of each thread, root first, as
	// conversations.replies answers them.
	threads   map[string][]map[string]any
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
	mux.HandleFunc("/api/reactions.add", f.reaction(false))
	mux.HandleFunc("/api/reactions.remove", f.reaction(true))
	mux.HandleFunc("/api/chat.meMessage", f.meMessage)
	mux.HandleFunc("/api/auth.test", f.authTest)
	mux.HandleFunc("/api/conversations.join", f.conversation(true))
	mux.HandleFunc("/api/conversations.leave", f.conversation(false))
	mux.HandleFunc("/api/conversations.replies", f.replies)
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
	p := Post{
		Channel: r.Form.Get("channel"), Text: r.Form.Get("text"), ThreadTS: r.Form.Get("thread_ts"),
		UnfurlLinks: r.Form.Get("unfurl_links"), UnfurlMedia: r.Form.Get("unfurl_media"),
	}
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
	f.remember(p.Channel, ts, p.ThreadTS, map[string]any{"ts": ts, "text": p.Text, "bot_id": "B00000001", "user": BotUser})
	f.lastTS = ts
	f.mu.Unlock()
	reply(w, map[string]any{"ok": true, "channel": p.Channel, "ts": ts})
}

func (f *Fake) reaction(removed bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != f.botToken {
			reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.noReactions {
			reply(w, map[string]any{"ok": false, "error": "missing_scope", "needed": "reactions:write"})
			return
		}
		f.reactions = append(f.reactions, Reaction{
			Channel: r.Form.Get("channel"), TS: r.Form.Get("timestamp"), Name: r.Form.Get("name"), Removed: removed,
		})
		reply(w, map[string]any{"ok": true})
	}
}

func (f *Fake) replies(w http.ResponseWriter, r *http.Request) {
	if bearer(r) != f.botToken {
		reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	msgs := append([]map[string]any(nil), f.threads[r.Form.Get("channel")+"/"+r.Form.Get("ts")]...)
	f.mu.Unlock()
	reply(w, map[string]any{"ok": true, "messages": msgs, "has_more": false})
}

// remember files a message under its thread, for conversations.replies.
// f.mu is held.
func (f *Fake) remember(channel, ts, threadTS string, m map[string]any) {
	if f.threads == nil {
		f.threads = map[string][]map[string]any{}
	}
	root := threadTS
	if root == "" {
		root = ts
	}
	key := channel + "/" + root
	f.threads[key] = append(f.threads[key], m)
}

func (f *Fake) authTest(w http.ResponseWriter, r *http.Request) {
	if bearer(r) != f.botToken {
		reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	reply(w, map[string]any{"ok": true, "user_id": BotUser, "user": "hikyaku", "team_id": "T00000000"})
}

func (f *Fake) conversation(join bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != f.botToken {
			reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		_ = r.ParseForm()
		ch := r.Form.Get("channel")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case join && f.private[ch]:
			reply(w, map[string]any{"ok": false, "error": "method_not_supported_for_channel_type"})
			return
		case join:
			f.joins = append(f.joins, ch)
		default:
			f.leaves = append(f.leaves, ch)
		}
		reply(w, map[string]any{"ok": true, "channel": map[string]any{"id": ch}})
	}
}

func (f *Fake) meMessage(w http.ResponseWriter, r *http.Request) {
	if bearer(r) != f.botToken {
		reply(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	f.seq++
	ts := fmt.Sprintf("1700000000.%06d", f.seq)
	f.mes = append(f.mes, Post{Channel: r.Form.Get("channel"), Text: r.Form.Get("text"), ThreadTS: r.Form.Get("thread_ts")})
	f.mu.Unlock()
	reply(w, map[string]any{"ok": true, "channel": r.Form.Get("channel"), "ts": ts})
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

// ReplyIn delivers a thread reply from user in a channel of the given type
// ("channel", "group" or "im"), and returns its ts.
func (f *Fake) ReplyIn(t *testing.T, channel, channelType, threadTS, user, text string) string {
	t.Helper()
	return f.deliver(t, map[string]any{"channel": channel, "user": user, "text": text, "thread_ts": threadTS,
		"channel_type": channelType})
}

// Say delivers a top-level message from user in channel, whose type is
// "im", "channel" or "group", and returns its ts.
func (f *Fake) Say(t *testing.T, channel, channelType, user, text string) string {
	t.Helper()
	return f.deliver(t, map[string]any{"channel": channel, "user": user, "text": text, "channel_type": channelType})
}

// Me delivers a /me message from user: top-level when threadTS is empty,
// in that thread otherwise. It returns its ts.
func (f *Fake) Me(t *testing.T, channel, threadTS, user, text string) string {
	t.Helper()
	e := map[string]any{"channel": channel, "user": user, "text": text, "channel_type": "im", "subtype": "me_message"}
	if threadTS != "" {
		e["thread_ts"] = threadTS
	}
	return f.deliver(t, e)
}

// BotMe delivers a top-level /me message from a bot in the DM channel,
// posted as user (an app can post as a person it acts for).
func (f *Fake) BotMe(t *testing.T, channel, user, text string) {
	t.Helper()
	f.deliver(t, map[string]any{"channel": channel, "bot_id": "B00000001", "user": user, "text": text,
		"channel_type": "im", "subtype": "me_message"})
}

// Bot delivers a top-level message from a bot in the DM channel.
func (f *Fake) Bot(t *testing.T, channel, text string) {
	t.Helper()
	f.deliver(t, map[string]any{"channel": channel, "bot_id": "B00000001", "text": text, "channel_type": "im"})
}

func (f *Fake) deliver(t *testing.T, event map[string]any) string {
	t.Helper()
	event["type"] = "message"
	return f.deliverEvent(t, event)
}

// deliverEvent sends an Events API event over every connection; a message
// is given the next ts, which is returned.
func (f *Fake) deliverEvent(t *testing.T, event map[string]any) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	ts := fmt.Sprintf("1700000000.%06d", f.seq)
	if event["type"] == "message" {
		event["ts"] = ts
		thread, _ := event["thread_ts"].(string)
		user, _ := event["user"].(string)
		text, _ := event["text"].(string)
		m := map[string]any{"ts": ts, "user": user, "text": text}
		if b, ok := event["bot_id"]; ok {
			m["bot_id"] = b
		}
		f.remember(event["channel"].(string), ts, thread, m)
	}
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

// Reactions returns every reactions.add and reactions.remove so far.
func (f *Fake) Reactions() []Reaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Reaction(nil), f.reactions...)
}

// ReactionsOn is the reactions on the message at ts now, in the order they
// were added.
func (f *Fake) ReactionsOn(ts string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var on []string
	for _, r := range f.reactions {
		if r.TS != ts {
			continue
		}
		on = slices.DeleteFunc(on, func(n string) bool { return n == r.Name })
		if !r.Removed {
			on = append(on, r.Name)
		}
	}
	return on
}

// NoReactionScope makes the reactions methods answer missing_scope, as for
// an app installed without reactions:write.
func (f *Fake) NoReactionScope() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noReactions = true
}

// MeMessages returns every chat.meMessage so far.
func (f *Fake) MeMessages() []Post {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Post(nil), f.mes...)
}

// Private makes conversations.join refuse the channel, as for a private one.
func (f *Fake) Private(channel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.private == nil {
		f.private = map[string]bool{}
	}
	f.private[channel] = true
}

// Joins are the channels the bot was asked to join.
func (f *Fake) Joins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.joins...)
}

// Leaves are the channels the bot was asked to leave.
func (f *Fake) Leaves() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.leaves...)
}

// Added delivers the event that user was added to channel by inviter.
func (f *Fake) Added(t *testing.T, channel, user, inviter string) {
	t.Helper()
	e := map[string]any{"type": "member_joined_channel", "channel": channel, "user": user, "channel_type": "C"}
	if inviter != "" {
		e["inviter"] = inviter
	}
	f.deliverEvent(t, e)
}
