/*
Package slackio is hikyaku's whole conversation with Slack: posting through
the Web API, and hearing thread replies over Socket Mode.

Nothing outside this package imports slack-go, so the service and its tests
see two small interfaces and plain values.
*/
package slackio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Status is the outcome a message reports in its header.
type Status string

// The statuses `send --status` accepts.
const (
	StatusNone Status = ""
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// ParseStatus checks a --status value.
func ParseStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusNone, StatusOK, StatusWarn, StatusFail:
		return Status(s), nil
	default:
		return "", fmt.Errorf("status %q: want ok, warn or fail", s)
	}
}

// Outgoing is a message to post.
type Outgoing struct {
	Text   string
	Title  string
	Status Status
	// From says where the message came from; it goes on a line under the
	// message, as plain text.
	From string
	// Question adds a line asking for the reply in the message's thread.
	Question bool
	// ThreadTS posts the message in that thread instead of starting one.
	ThreadTS string
	// NoUnfurl stops Slack fetching the links in the message to preview
	// them, so a link in text hikyaku did not write is never fetched
	// unless someone clicks it.
	NoUnfurl bool
}

// Neutralize stops text from mentioning anyone: Slack's special mentions
// (<!channel>, <!here>, <!subteam^…>), user mentions (<@U…>) and channel
// links (<#C…>) are shown as text instead. Ordinary links are kept.
func Neutralize(text string) string {
	return mentionOpen.Replace(text)
}

var mentionOpen = strings.NewReplacer("<!", "&lt;!", "<@", "&lt;@", "<#", "&lt;#") //nolint:gochecknoglobals // built once

// ReplyHint is the line a question carries.
const ReplyHint = "Reply in this thread to answer."

// Incoming is a person's new message, as Slack reported it: a reply in a
// thread (ThreadTS set) or a top-level message (ThreadTS empty).
type Incoming struct {
	Channel string
	// ChannelType is "im" for a DM with the bot, "channel" or "group"
	// for a public or private channel.
	ChannelType string
	ThreadTS    string
	TS          string
	User        string
	Text        string
	// Joined marks the event that User was added to Channel, by Inviter
	// (empty when they joined themselves); it carries no text.
	Joined  bool
	Inviter string
}

// InDM reports whether the message was written in a DM with the bot.
func (m Incoming) InDM() bool { return m.ChannelType == "im" }

// Poster posts messages, reacts to them, and opens the DM with a user.
type Poster interface {
	OpenDM(ctx context.Context, user string) (channel string, err error)
	Post(ctx context.Context, channel string, msg Outgoing) (postedChannel, ts string, err error)
	// React adds the emoji (a name, without colons) to the message at ts;
	// Unreact takes it off. Adding one already there, or taking off one
	// that is not, is not an error.
	React(ctx context.Context, channel, ts, emoji string) error
	Unreact(ctx context.Context, channel, ts, emoji string) error
	// Me posts text as a /me message, top-level in channel.
	Me(ctx context.Context, channel, text string) error
	// Join joins a public channel; Leave leaves a channel. Self is the
	// bot's own user ID.
	Join(ctx context.Context, channel string) error
	Leave(ctx context.Context, channel string) error
	Self(ctx context.Context) (string, error)
	// Thread is the messages of the thread rooted at ts, oldest first, at
	// most the last 50.
	Thread(ctx context.Context, channel, ts string) ([]ThreadMessage, error)
	// Upload shares a file in channel, in the thread at threadTS when set.
	Upload(ctx context.Context, channel, threadTS, filename, title string, data []byte) error
}

// ThreadMessage is one message of a thread; Bot marks one a bot posted.
type ThreadMessage struct {
	User, Text, TS string
	Bot            bool
}

// ErrNotPublic is wrapped by Join for a channel the bot cannot join by
// itself: a private one, which has to invite it.
var ErrNotPublic = errors.New("not a public channel")

// ErrMissingScope is wrapped by an error from a call the app's token has no
// scope for, such as React before the app is reinstalled with
// reactions:write.
var ErrMissingScope = errors.New("missing scope")

// Listener holds the Socket Mode connection and reports people's new
// messages.
type Listener interface {
	Listen(ctx context.Context, replies chan<- Incoming, connected func(bool)) error
}

// Client is the real Poster and Listener.
type Client struct {
	api *slack.Client
	log *slog.Logger
	// retryMin and retryMax bound the wait between Socket Mode attempts when
	// slack-go gives up on a connection.
	retryMin, retryMax time.Duration
}

// Options configures a Client.
type Options struct {
	BotToken string
	AppToken string
	// APIURL overrides https://slack.com/api/, for tests. Ends in a slash.
	APIURL string
	Log    *slog.Logger
}

// New builds a Client from the two tokens.
func New(o Options) *Client {
	opts := []slack.Option{slack.OptionAppLevelToken(o.AppToken)}
	if o.APIURL != "" {
		opts = append(opts, slack.OptionAPIURL(o.APIURL))
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{api: slack.New(o.BotToken, opts...), log: log, retryMin: time.Second, retryMax: 5 * time.Minute}
}

// OpenDM returns the ID of the bot's DM with user, opening it if needed.
func (c *Client) OpenDM(ctx context.Context, user string) (string, error) {
	ch, _, _, err := c.api.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{user}})
	if err != nil {
		return "", fmt.Errorf("open DM with %s: %w", user, err)
	}
	return ch.ID, nil
}

// Post posts msg to channel and returns where it landed.
func (c *Client) Post(ctx context.Context, channel string, msg Outgoing) (string, string, error) {
	fallback, blocks := Compose(msg)
	opts := []slack.MsgOption{slack.MsgOptionText(fallback, false)}
	if len(blocks) > 0 {
		opts = append(opts, slack.MsgOptionBlocks(blocks...))
	}
	if msg.ThreadTS != "" {
		opts = append(opts, slack.MsgOptionTS(msg.ThreadTS))
	}
	if msg.NoUnfurl {
		opts = append(opts, slack.MsgOptionDisableLinkUnfurl(), slack.MsgOptionDisableMediaUnfurl())
	}
	ch, ts, err := c.api.PostMessageContext(ctx, channel, opts...)
	if err != nil {
		return "", "", fmt.Errorf("post to %s: %w", channel, err)
	}
	return ch, ts, nil
}

// React adds a reaction.
func (c *Client) React(ctx context.Context, channel, ts, emoji string) error {
	err := c.api.AddReactionContext(ctx, emoji, slack.ItemRef{Channel: channel, Timestamp: ts})
	return reactionErr("add reaction", err, "already_reacted")
}

// Unreact removes a reaction.
func (c *Client) Unreact(ctx context.Context, channel, ts, emoji string) error {
	err := c.api.RemoveReactionContext(ctx, emoji, slack.ItemRef{Channel: channel, Timestamp: ts})
	return reactionErr("remove reaction", err, "no_reaction")
}

// reactionErr is nil for err and for Slack's answer that the change was
// already so (harmless), and wraps ErrMissingScope when the token lacks
// the scope.
func reactionErr(what string, err error, harmless string) error {
	switch {
	case err == nil, strings.Contains(err.Error(), harmless):
		return nil
	case strings.Contains(err.Error(), "missing_scope"):
		return fmt.Errorf("%s: %w", what, ErrMissingScope)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Join joins a public channel.
func (c *Client) Join(ctx context.Context, channel string) error {
	_, _, _, err := c.api.JoinConversationContext(ctx, channel)
	return channelErr("join "+channel, err)
}

// Leave leaves a channel; one the bot is not in is not an error.
func (c *Client) Leave(ctx context.Context, channel string) error {
	_, err := c.api.LeaveConversationContext(ctx, channel)
	if err != nil && strings.Contains(err.Error(), "not_in_channel") {
		return nil
	}
	return channelErr("leave "+channel, err)
}

func channelErr(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "missing_scope"):
		return fmt.Errorf("%s: %w", what, ErrMissingScope)
	case strings.Contains(err.Error(), "method_not_supported_for_channel_type"),
		strings.Contains(err.Error(), "channel_not_found"):
		return fmt.Errorf("%s: %w", what, ErrNotPublic)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Thread reads a thread (conversations.replies, under the history scopes).
func (c *Client) Thread(ctx context.Context, channel, ts string) ([]ThreadMessage, error) {
	msgs, _, _, err := c.api.GetConversationRepliesContext(ctx,
		&slack.GetConversationRepliesParameters{ChannelID: channel, Timestamp: ts, Limit: 50})
	if err != nil {
		return nil, channelErr("read a thread", err)
	}
	out := make([]ThreadMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, ThreadMessage{User: m.User, Text: m.Text, TS: m.Timestamp, Bot: m.BotID != ""})
	}
	if len(out) > 50 {
		out = out[len(out)-50:]
	}
	return out, nil
}

// Upload shares a file (files:write), with Slack's three-step upload.
func (c *Client) Upload(ctx context.Context, channel, threadTS, filename, title string, data []byte) error {
	_, err := c.api.UploadFileContext(ctx, slack.UploadFileParameters{
		Reader: bytes.NewReader(data), FileSize: len(data), Filename: filename, Title: title,
		Channel: channel, ThreadTimestamp: threadTS,
	})
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "missing_scope"):
		return fmt.Errorf("upload %s: %w", filename, ErrMissingScope)
	}
	return fmt.Errorf("upload %s: %w", filename, err)
}

// Self is the bot's own user ID, from auth.test, which needs no scope.
func (c *Client) Self(ctx context.Context) (string, error) {
	r, err := c.api.AuthTestContext(ctx)
	if err != nil {
		return "", fmt.Errorf("auth.test: %w", err)
	}
	return r.UserID, nil
}

// Me posts a /me message.
func (c *Client) Me(ctx context.Context, channel, text string) error {
	if _, _, err := c.api.PostMessageContext(ctx, channel, slack.MsgOptionMeMessage(), slack.MsgOptionText(text, false)); err != nil {
		return fmt.Errorf("post /me to %s: %w", channel, err)
	}
	return nil
}

// sectionLimit is Slack's limit on a section block's text.
const sectionLimit = 3000

// statusEmoji is the emoji a status puts before the title.
var statusEmoji = map[Status]string{ //nolint:gochecknoglobals // a fixed table
	StatusOK:   ":white_check_mark:",
	StatusWarn: ":warning:",
	StatusFail: ":x:",
}

// Compose turns a message into the fallback text Slack shows in a
// notification and, when there is a title, a status, an origin or a reply
// hint, the blocks that carry them. A message with none of these is plain
// mrkdwn text. The fallback is always the agent's own words, with the title
// before them, never the origin or the hint.
func Compose(m Outgoing) (string, []slack.Block) {
	foot := footer(m)
	hasHead := m.Title != "" || m.Status != StatusNone
	if !hasHead && foot == nil {
		return m.Text, nil
	}
	var blocks []slack.Block
	fallback := m.Text
	if hasHead {
		title := m.Title
		if title == "" {
			title = string(m.Status)
		}
		head := title
		if e, ok := statusEmoji[m.Status]; ok {
			head = e + " " + title
		}
		blocks = append(blocks, slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, head, true, false)))
		fallback = title
		if m.Text != "" {
			fallback = title + ": " + m.Text
		}
	}
	for _, part := range split(m.Text, sectionLimit) {
		blocks = append(blocks, slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, part, false, false), nil, nil))
	}
	if foot != nil {
		blocks = append(blocks, foot)
	}
	return fallback, blocks
}

// fromLimit bounds the origin line; Slack allows 2000 characters in a
// context element, and an origin is a few words.
const fromLimit = 200

// footer is the context block under a message: where it came from, and on a
// question, where to reply. It is one plain-text element, so an origin
// cannot mention anyone or carry a link, and a copied message reads as one
// line: Slack joins separate elements with a bare space when copying.
func footer(m Outgoing) *slack.ContextBlock {
	var parts []string
	if from := oneLine(m.From, fromLimit); from != "" {
		parts = append(parts, from)
	}
	if m.Question {
		parts = append(parts, ReplyHint)
	}
	if parts == nil {
		return nil
	}
	return slack.NewContextBlock("", slack.NewTextBlockObject(slack.PlainTextType, strings.Join(parts, " · "), false, false))
}

// oneLine folds s onto one line and cuts it to at most limit runes.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit-1]) + "…"
}

// split cuts s into pieces of at most limit bytes, at a line break where
// there is one, and never inside a UTF-8 sequence.
func split(s string, limit int) []string {
	var out []string
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		for i := cut; i > limit/2; i-- {
			if s[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// ErrAuth is returned by Listen when Slack refuses the tokens; retrying will
// not help.
var ErrAuth = errors.New("slack refused the tokens")

// fatalAuth is what slack-go returns when a token is wrong or revoked.
var fatalAuth = map[string]bool{ //nolint:gochecknoglobals // a fixed table
	"invalid_auth": true, "account_inactive": true, "not_authed": true, "token_revoked": true,
}

// Listen holds the Socket Mode connection until ctx is cancelled, sending
// each thread reply to replies. slack-go reconnects a dropped connection
// itself; when it gives up, Listen waits and starts again, doubling the wait
// up to retryMax. It returns ErrAuth, wrapped, when the tokens are refused.
func (c *Client) Listen(ctx context.Context, replies chan<- Incoming, connected func(bool)) error {
	wait := c.retryMin
	for {
		err := c.listenOnce(ctx, replies, connected)
		connected(false)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // a cancelled context is the caller asking to stop, not a failure
		}
		if errors.Is(err, ErrAuth) {
			return err
		}
		c.log.Warn("socket mode connection ended; retrying", "err", err, "in", wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		wait = min(wait*2, c.retryMax)
	}
}

func (c *Client) listenOnce(ctx context.Context, replies chan<- Incoming, connected func(bool)) error {
	sm := socketmode.New(c.api)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	authFailed := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-sm.Events:
				if !ok {
					return
				}
				c.event(ctx, sm, evt, replies, connected, authFailed)
			}
		}
	}()
	err := sm.RunContext(ctx)
	select {
	case <-authFailed:
		return fmt.Errorf("%w", ErrAuth)
	default:
	}
	if err != nil && fatalAuth[err.Error()] {
		return fmt.Errorf("%w: %s", ErrAuth, err.Error())
	}
	if err != nil {
		return fmt.Errorf("socket mode: %w", err)
	}
	return nil
}

func (c *Client) event(ctx context.Context, sm *socketmode.Client, evt socketmode.Event, replies chan<- Incoming,
	connected func(bool), authFailed chan<- struct{}) {
	switch evt.Type {
	case socketmode.EventTypeConnected:
		connected(true)
		c.log.Info("connected to slack")
	case socketmode.EventTypeConnecting, socketmode.EventTypeConnectionError, socketmode.EventTypeDisconnect:
		connected(false)
	case socketmode.EventTypeInvalidAuth:
		select {
		case authFailed <- struct{}{}:
		default:
		}
	case socketmode.EventTypeEventsAPI:
		if evt.Request != nil {
			if err := sm.AckCtx(ctx, evt.Request.EnvelopeID, nil); err != nil {
				c.log.Debug("ack", "err", err)
			}
		}
		api, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		logEvent(c.log, api)
		if j, ok := api.InnerEvent.Data.(*slackevents.MemberJoinedChannelEvent); ok {
			in := Incoming{Channel: j.Channel, ChannelType: j.ChannelType, User: j.User, Joined: true, Inviter: j.Inviter}
			select {
			case replies <- in:
			case <-ctx.Done():
			}
			return
		}
		if in, ok := personMessage(api); ok {
			select {
			case replies <- in:
			case <-ctx.Done():
			}
		}
	}
}

// personMessage picks out a person's new message, a /me included. Edits,
// deletions and bot messages (our own included) are not passed on. A
// message that starts a thread is reported as top-level.
func personMessage(api slackevents.EventsAPIEvent) (Incoming, bool) {
	m, ok := api.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok || (m.SubType != "" && m.SubType != "me_message") || m.BotID != "" || m.User == "" {
		return Incoming{}, false
	}
	thread := m.ThreadTimeStamp
	if thread == m.TimeStamp {
		thread = ""
	}
	return Incoming{
		Channel: m.Channel, ChannelType: m.ChannelType, ThreadTS: thread, TS: m.TimeStamp, User: m.User, Text: m.Text,
	}, true
}

// logEvent records at debug level what kind of event arrived, so a reply
// that never reaches an agent can be traced. It logs no text and no user.
func logEvent(log *slog.Logger, api slackevents.EventsAPIEvent) {
	m, ok := api.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok {
		log.Debug("slack event", "type", api.InnerEvent.Type)
		return
	}
	log.Debug("slack event", "type", api.InnerEvent.Type, "subtype", m.SubType,
		"channel_type", m.ChannelType, "in_thread", m.ThreadTimeStamp != "" && m.ThreadTimeStamp != m.TimeStamp,
		"from_bot", m.BotID != "")
}
