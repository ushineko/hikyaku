package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ushineko/hikyaku/internal/access"
	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/slackio"
)

// Roles (spec 009): admins are a setting; users are the access list,
// changed only from the desktop.

func (s *Service) isAdmin(user string) bool { return user != "" && slices.Contains(s.admins, user) }

// mayTalk is whether user may talk to the bot in a channel it is in.
func (s *Service) mayTalk(user string) bool {
	if s.isAdmin(user) {
		return true
	}
	_, ok := s.users().Get(user)
	return ok
}

// mayDM is whether a user who is not an admin may DM the bot and be
// answered.
func (s *Service) mayDM(user string) bool {
	u, ok := s.users().Get(user)
	return ok && u.DM
}

func (s *Service) users() *access.List {
	if s.access == nil {
		return &access.List{}
	}
	return s.access
}

// Notes about channels.
const (
	noteOnlyAdminLeaves = "Only an admin can ask me to leave."
	notePrivate         = "I can't join a private channel myself; invite me from inside it."
	noteNotAnswering    = "I'm not answering right now."
	noteNoScope         = "The Slack app is missing a scope for that (channels:join, channels:manage or groups:write). " +
		"Add it and reinstall the app."
)

// dmCommand matches an admin's `join <#C…>` or `leave <#C…>` in the DM
// (R5). Slack writes a channel link as <#C123|name> or <#C123>.
var dmCommand = regexp.MustCompile(`(?i)^\s*(join|leave)\s+<#([CG][A-Z0-9]+)(?:\|[^>]*)?>\s*$`)

// adminCommand handles a DM that is exactly a join or leave command from
// an admin, and reports whether it was one.
func (s *Service) adminCommand(ctx context.Context, m slackio.Incoming) bool {
	c := dmCommand.FindStringSubmatch(m.Text)
	if c == nil || !s.isAdmin(m.User) {
		return false
	}
	verb, channel := strings.ToLower(c[1]), c[2]
	var err error
	var done string
	if verb == "join" {
		err, done = s.poster.Join(ctx, channel), fmt.Sprintf("Joined <#%s>.", channel)
	} else {
		err, done = s.poster.Leave(ctx, channel), fmt.Sprintf("Left <#%s>.", channel)
	}
	s.log.Info("an admin asked the bot to "+verb+" a channel", "channel", channel, "admin", m.User, "ok", err == nil)
	switch {
	case err == nil:
		go s.answer(ctx, m, slackio.Outgoing{Text: done})
	case errors.Is(err, slackio.ErrNotPublic):
		go s.answer(ctx, m, slackio.Outgoing{Text: notePrivate})
	case errors.Is(err, slackio.ErrMissingScope):
		go s.answer(ctx, m, slackio.Outgoing{Text: noteNoScope})
	default:
		s.log.Warn(verb+" a channel", "err", s.redact.String(err.Error()))
		go s.answer(ctx, m, slackio.Outgoing{Text: "That didn't work. The service's log says why."})
	}
	return true
}

// channelMessage handles a top-level message in a channel the bot is in
// (R2): only an @-mention from an admin or a user counts; anyone else, and
// anything without a mention, gets nothing.
func (s *Service) channelMessage(ctx context.Context, m slackio.Incoming) {
	mention := "<@" + s.selfID() + ">"
	if !s.mayTalk(m.User) {
		s.log.Warn("ignored a mention from a user who has no access", "user", m.User)
		return
	}
	text := strings.TrimSpace(strings.ReplaceAll(m.Text, mention, ""))
	if strings.EqualFold(strings.Trim(text, ".! "), "leave") {
		if !s.isAdmin(m.User) {
			go s.answer(ctx, m, slackio.Outgoing{Text: noteOnlyAdminLeaves})
			return
		}
		if err := s.poster.Leave(ctx, m.Channel); err != nil {
			s.log.Warn("leave a channel", "err", s.redact.String(err.Error()))
			go s.answer(ctx, m, slackio.Outgoing{Text: noteNoScope})
			return
		}
		s.log.Info("an admin asked the bot to leave a channel", "channel", m.Channel, "admin", m.User)
		return
	}
	if s.desk == nil {
		return
	}
	// Answer where it was asked: beside a top-level mention, in the thread
	// of one in a thread, with the thread so far as context. Notes go in
	// the asking message's thread either way.
	q := question{channel: m.Channel, root: m.TS, msgTS: m.TS, topLevel: m.ThreadTS == "", text: text, asker: m.User}
	if m.ThreadTS != "" {
		q.root = m.ThreadTS
		q.thread = s.threadLines(ctx, m.Channel, m.ThreadTS, m.TS)
	}
	// An admin gets the full answerer; anyone else, a run that reads
	// nothing (R3).
	q.plain = !s.isAdmin(m.User)
	if !s.enqueue(ctx, q, true) {
		go s.answer(ctx, m, slackio.Outgoing{Text: noteNotAnswering})
	}
}

// joined handles the bot being added to a channel (R5): by someone who is
// not an admin, it leaves at once and tells the admins.
func (s *Service) joined(ctx context.Context, m slackio.Incoming) {
	if m.User != s.selfID() {
		return
	}
	if m.Inviter == "" || s.isAdmin(m.Inviter) {
		s.log.Info("the bot was added to a channel", "channel", m.Channel, "by", m.Inviter)
		return
	}
	s.log.Warn("the bot was added to a channel by someone who is not an admin; leaving",
		"channel", m.Channel, "by", m.Inviter)
	if err := s.poster.Leave(ctx, m.Channel); err != nil {
		s.log.Warn("leave a channel", "err", s.redact.String(err.Error()))
	}
	for _, admin := range s.admins {
		dm, err := s.poster.OpenDM(ctx, admin)
		if err != nil {
			s.log.Warn("open an admin's DM", "err", s.redact.String(err.Error()))
			continue
		}
		s.post(ctx, dm, slackio.Outgoing{Text: fmt.Sprintf(
			"<@%s> added me to <#%s>. Only an admin can, so I left.", m.Inviter, m.Channel)})
	}
}

// askerInOwnThread is whether m is a reply, in the thread under one of the
// bot's top-level answers in a channel, from the person who asked: they
// may follow up without a mention (spec 009, R2).
func (s *Service) askerInOwnThread(m slackio.Incoming) bool {
	if s.desk == nil || m.ThreadTS == "" {
		return false
	}
	d := s.desk
	d.mu.Lock()
	defer d.mu.Unlock()
	ex := d.threads[m.ThreadTS]
	return ex != nil && ex.asker == m.User && ex.channel == m.Channel
}

// mentionsMe is whether text @-mentions the bot.
func (s *Service) mentionsMe(text string) bool {
	self := s.selfID()
	return self != "" && strings.Contains(text, "<@"+self+">")
}

// threadLines is a channel thread's messages before the one at skip, for a
// run's prompt; a thread that cannot be read gives none.
func (s *Service) threadLines(ctx context.Context, channel, root, skip string) []answerer.Line {
	msgs, err := s.poster.Thread(ctx, channel, root)
	if err != nil {
		s.log.Warn("read a thread", "err", s.redact.String(err.Error()))
		return nil
	}
	self := "<@" + s.selfID() + ">"
	var out []answerer.Line
	for _, m := range msgs {
		if m.TS == skip {
			break
		}
		who := "person " + m.User
		if m.Bot {
			who = "you (the bot)"
		}
		out = append(out, answerer.Line{Who: who, Text: strings.ReplaceAll(m.Text, self, "@bot")})
	}
	return out
}

func (s *Service) selfID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.self
}

// turnOf is a question and its answer, for the next run's earlier turns.
func turnOf(q question, answer string) answerer.Turn {
	return answerer.Turn{Question: q.text, Answer: answer}
}

// users is `hikyaku users` (R1): list, add, remove, and the DM grant.
func (s *Service) usersOp(req ipc.Request) ipc.Response {
	if s.access == nil {
		return ipc.Fail(ipc.CodeNotConfigured, "this service keeps no users")
	}
	id := strings.ToUpper(strings.TrimSpace(req.User))
	if s.isAdmin(id) && req.Switch != "" {
		return ipc.Fail(ipc.CodeBadRequest, id+" is an admin; admins are set in the settings")
	}
	var err error
	switch req.Switch {
	case "":
	case "add":
		u, _ := s.access.Get(id)
		u.ID = id
		u.DM = u.DM || req.DM
		err = s.access.Put(u)
	case "remove":
		var was bool
		if was, err = s.access.Remove(id); err == nil && !was {
			return ipc.Fail(ipc.CodeBadRequest, id+" is not a user")
		}
	case "dm-on", "dm-off":
		u, ok := s.access.Get(id)
		if !ok {
			return ipc.Fail(ipc.CodeBadRequest, id+" is not a user; add them first")
		}
		u.DM = req.Switch == "dm-on"
		err = s.access.Put(u)
	default:
		return ipc.Fail(ipc.CodeBadRequest, "unknown users operation "+req.Switch)
	}
	if errors.Is(err, access.ErrBadID) {
		return ipc.Fail(ipc.CodeBadRequest, err.Error())
	}
	if err != nil {
		s.log.Error("save users", "err", err)
		return ipc.Fail(ipc.CodeInternal, s.redact.String(err.Error()))
	}
	if req.Switch != "" {
		s.log.Info("users changed", "op", req.Switch, "user", id)
	}
	return ipc.Response{OK: true, Users: s.userList()}
}

func (s *Service) userList() []ipc.UserInfo {
	spent := map[string]float64{}
	if d := s.desk; d != nil {
		d.mu.Lock()
		d.todayLocked(s.now())
		for k, v := range d.st.SpentBy {
			spent[k] = v
		}
		d.mu.Unlock()
	}
	var out []ipc.UserInfo
	for _, a := range s.admins {
		out = append(out, ipc.UserInfo{ID: a, Admin: true, DM: true})
	}
	for _, u := range s.users().Users() {
		out = append(out, ipc.UserInfo{ID: u.ID, DM: u.DM, Spent: spent[u.ID]})
	}
	return out
}
