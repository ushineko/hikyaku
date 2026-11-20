/*
Package ipc is the conversation between the hikyaku CLI and the running
service, over a Unix socket in a directory only the user can enter.

One request per connection: the client writes a JSON line and reads a JSON
line back. A `wait` holds the connection open until a reply, its timeout, or
the client going away.
*/
package ipc

import (
	"time"

	"github.com/ushineko/hikyaku/internal/store"
)

// Operations a client can ask for.
const (
	OpSend   = "send"
	OpWait   = "wait"
	OpListen = "listen"
	OpInbox  = "inbox"
	OpStatus = "status"
)

// Error codes a response can carry, so the CLI can choose its exit code
// without parsing a message.
const (
	CodeTimeout       = "timeout"
	CodeUnknownHandle = "unknown_handle"
	CodeBadRequest    = "bad_request"
	CodeNotConfigured = "not_configured"
	CodeSlack         = "slack"
	CodeInternal      = "internal"
)

// maxLine bounds a request or a response. Slack's own limit on a message is
// 40,000 characters; a megabyte is room for that and a full inbox.
const maxLine = 1 << 20

// Request is what the CLI sends.
type Request struct {
	Op      string `json:"op"`
	Text    string `json:"text,omitempty"`
	Title   string `json:"title,omitempty"`
	Status  string `json:"status,omitempty"`
	Channel string `json:"channel,omitempty"`
	// From is the origin line under the message.
	From string `json:"from,omitempty"`
	// Question marks a message posted by `ask`, which carries a reply hint.
	Question bool `json:"question,omitempty"`
	// Name is a listener's name.
	Name string `json:"name,omitempty"`
	// Thread is the handle of a message whose thread to post in.
	Thread  string        `json:"thread,omitempty"`
	Handle  string        `json:"handle,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Response is what the service answers.
type Response struct {
	OK      bool          `json:"ok"`
	Code    string        `json:"code,omitempty"`
	Error   string        `json:"error,omitempty"`
	Handle  string        `json:"handle,omitempty"`
	Replies []store.Reply `json:"replies,omitempty"`
	Status  *Status       `json:"status,omitempty"`
}

// Status is the service's account of itself.
type Status struct {
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	Connected bool   `json:"connected"`
	Owner     string `json:"owner,omitempty"`
	Repliers  int    `json:"repliers"`
	// Listeners are the names of the agents waiting for an instruction.
	Listeners []string `json:"listeners,omitempty"`
}

// Fail builds an error response.
func Fail(code, msg string) Response { return Response{Code: code, Error: msg} }
