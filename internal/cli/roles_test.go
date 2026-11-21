package cli_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/slacktest"
)

const (
	channel   = "C00000001"
	user      = "U00000003"
	mentioned = "<@" + slacktest.BotUser + "> "
	plainMark = plainRunnerMark + "You answer questions for a Slack bot, for someone in Slack who is not the bot's owner."
)

// rolesHarness is the answerer on, with one user listed and runs that
// answer "answer to <question>".
func rolesHarness(t *testing.T, dm bool) (*harness, *fakeRunner) {
	t.Helper()
	h, r := answererHarness(t, "")
	r.answer = func(_ context.Context, p string) (answerer.Result, error) { return answerers(p) }
	switchAnswerer(t, h, "on")
	args := []string{"users", "add", user}
	if dm {
		args = append(args, "--dm")
	}
	code, _, errOut := h.run("", args...)
	require.Equal(t, 0, code, errOut)
	return h, r
}

func (h *harness) noPostIn(ts string) {
	h.t.Helper()
	time.Sleep(300 * time.Millisecond)
	for _, p := range h.fake.Posts() {
		assert.NotEqual(h.t, ts, p.ThreadTS, "a reply was posted: %+v", p)
	}
}

// topLevelIn waits for a top-level post in a channel with this text.
func (h *harness) topLevelIn(ch, text string) slacktest.Post {
	h.t.Helper()
	var found slacktest.Post
	require.Eventually(h.t, func() bool {
		for _, p := range h.fake.Posts() {
			if p.Channel == ch && p.ThreadTS == "" && p.Text == text {
				found = p
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "no top-level %q in %s: %+v", text, ch, h.fake.Posts())
	return found
}

// AC1: in a channel, only an @-mention from an admin or a user counts. A
// top-level one is answered top-level, with reactions for progress; a
// thread needs its own mention, and is answered in, with the thread as
// context. A user's answer reads nothing.
func TestAChannelHearsOnlyMentionsFromPeopleWithAccess(t *testing.T) {
	h, r := rolesHarness(t, false)

	ts := h.fake.Say(t, channel, "channel", stranger, mentioned+"what's up?")
	h.noPostIn(ts)
	ts = h.fake.Say(t, channel, "channel", user, "no mention here")
	h.noPostIn(ts)
	assert.Empty(t, r.runs())
	assert.Contains(t, h.serveLog.String(), "ignored a mention from a user who has no access")

	ts = h.fake.Say(t, channel, "channel", user, mentioned+"what is a monad?")
	h.topLevelIn(channel, "answer to what is a monad?")
	h.reactedWith(ts, "white_check_mark")
	require.Len(t, r.runs(), 1)
	assert.Contains(t, r.runs()[0], plainMark, "a user's run reads nothing")
	h.noPostIn(ts)

	answer := h.fake.LastTS()
	h.fake.ReplyIn(t, channel, "channel", answer, owner, "nice")
	noRunFor(t, r, 1) // not the asker, and no mention

	h.fake.ReplyIn(t, channel, "channel", answer, user, "and a functor?")
	h.postIn(answer, is("answer to and a functor?"))
	assert.Contains(t, r.runs()[1], plainMark, "the asker needs no mention under the bot's answer")
	assert.Contains(t, r.runs()[1], "you (the bot): answer to what is a monad?\nperson "+owner+": nice")
	assert.NotContains(t, r.runs()[1], "person "+user+": and a functor?", "the question is not repeated as thread context")

	h.fake.ReplyIn(t, channel, "channel", answer, owner, mentioned+"and a monoid?")
	h.postIn(answer, is("answer to and a monoid?"))
	assert.NotContains(t, r.runs()[2], plainRunnerMark, "an admin's mention gets the full answerer")

	other := h.fake.Say(t, channel, "channel", owner, "a thread the bot did not start")
	h.fake.ReplyIn(t, channel, "channel", other, user, "anyone?")
	h.fake.ReplyIn(t, channel, "channel", answer, stranger, mentioned+"me too")
	noRunFor(t, r, 3)
}

// AC2: a user's DM is answered only with the DM grant, and reads nothing;
// an admin's DM is the full answerer.
func TestDMsNeedTheGrant(t *testing.T) {
	h, r := rolesHarness(t, false)

	ts := h.fake.Say(t, slacktest.DMChannel, "im", user, "hello?")
	h.noPostIn(ts)
	assert.Empty(t, r.runs())

	code, _, _ := h.run("", "users", "dm", user, "on")
	require.Equal(t, 0, code)
	ts = h.fake.Say(t, slacktest.DMChannel, "im", user, "hello?")
	h.postIn(ts, is("answer to hello?"))
	assert.Contains(t, r.runs()[0], plainMark)

	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "release?")
	h.postIn(ts, is("answer to release?"))
	assert.NotContains(t, r.runs()[1], plainRunnerMark, "an admin gets the full answerer")

	code, _, _ = h.run("", "users", "remove", user)
	require.Equal(t, 0, code)
	ts = h.fake.Say(t, slacktest.DMChannel, "im", user, "still there?")
	h.noPostIn(ts)
	noRunFor(t, r, 2)
}

// AC4: an admin's channel question gets the full answerer, posted where it
// was asked, with no draft or approval (owner, 2026-11-20).
func TestAnAdminsChannelQuestionIsAnsweredDirectly(t *testing.T) {
	h, r := rolesHarness(t, false)

	q := h.fake.Say(t, channel, "channel", owner, mentioned+"status of the release?")
	got := h.topLevelIn(channel, "answer to status of the release?")
	require.Len(t, footer(got), 1, "labelled as an automated answer")
	h.reactedWith(q, "white_check_mark")
	require.Len(t, r.runs(), 1)
	assert.NotContains(t, r.runs()[0], plainRunnerMark, "an admin gets the full answerer")
	for _, p := range h.fake.Posts() {
		assert.Equal(t, channel, p.Channel, "nothing went to a DM")
	}
}

// AC5: join and leave from an admin's DM, leave from a channel, and an
// invite from someone who is not an admin.
func TestAdminsDecideWhereTheBotIs(t *testing.T) {
	h, _ := rolesHarness(t, false)

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "join <#C00000002|general>")
	h.noteIn(ts, "Joined <#C00000002>.")
	assert.Equal(t, []string{"C00000002"}, h.fake.Joins())
	h.fake.Private("C00000003")
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "join <#C00000003>")
	h.noteIn(ts, "I can't join a private channel myself; invite me from inside it.")
	ts = h.fake.Say(t, slacktest.DMChannel, "im", owner, "leave <#C00000002|general>")
	h.noteIn(ts, "Left <#C00000002>.")

	ts = h.fake.Say(t, channel, "channel", user, mentioned+"leave")
	h.noteIn(ts, "Only an admin can ask me to leave.")
	h.fake.Say(t, channel, "channel", owner, mentioned+"leave")
	require.Eventually(t, func() bool { return len(h.fake.Leaves()) == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, channel, h.fake.Leaves()[1])

	h.fake.Added(t, "C00000004", slacktest.BotUser, owner)
	h.fake.Added(t, "C00000005", slacktest.BotUser, stranger)
	h.postIn("", is("<@"+stranger+"> added me to <#C00000005>. Only an admin can, so I left."))
	assert.Equal(t, []string{"C00000002", channel, "C00000005"}, h.fake.Leaves(), "only the stranger's invite was left")

	ts = h.fake.Say(t, slacktest.DMChannel, "im", user, "join <#C00000006>")
	h.noPostIn(ts)
	assert.Equal(t, []string{"C00000002"}, h.fake.Joins(), "a user cannot make the bot join")
}

// AC6: a user's runs stop at $1 a day; status shows what users spent.
func TestAUsersDayIsCapped(t *testing.T) {
	h, r := rolesHarness(t, false)
	r.answer = answers("fine", 0.6)

	for _, q := range []string{"one", "two"} {
		ts := h.fake.Say(t, channel, "channel", user, mentioned+q)
		h.reactedWith(ts, "white_check_mark")
	}
	ts := h.fake.Say(t, channel, "channel", user, mentioned+"three")
	h.noteIn(ts, "That's my limit for you today. Ask again tomorrow.")
	noRunFor(t, r, 2)

	ts = h.fake.Say(t, channel, "channel", owner, mentioned+"admins are not capped per user")
	h.reactedWith(ts, "white_check_mark")
	_, out, _ := h.run("", "status")
	assert.Contains(t, out, "$1.80 of $5.00 spent today ($1.20 by users)")
	_, out, _ = h.run("", "users")
	assert.Contains(t, out, user+"  user, channels only; $1.20 today")
}

// AC7: the users list survives a restart, and nothing typed in Slack
// changes it.
func TestAccessChangesOnlyAtTheDesk(t *testing.T) {
	h, _ := rolesHarness(t, true)
	_, out, _ := h.run("", "users")
	assert.Equal(t, owner+"  admin, channels and DM; $0.00 today\n"+user+"  user, channels and DM; $0.00 today\n", out)

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "allow <@U00000004> to DM you")
	h.postIn(ts, startsWith("answer to"))
	h.restart()
	_, after, _ := h.run("", "users")
	assert.Equal(t, out, after, "a message in Slack changed nothing, and the list survived a restart")

	code, _, errOut := h.run("", "users", "add", owner)
	assert.NotEqual(t, 0, code)
	assert.Contains(t, errOut, "is an admin")
	code, _, errOut = h.run("", "users", "add", "not-an-id")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, errOut, "not a Slack user ID")
}

// Spec 009: an answer reaches Slack without absolute paths from this
// machine, and the prompt asks for repository-relative ones.
func TestAnAnswerShowsNoPathsFromTheMachine(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	h, r := answererHarness(t, "")
	r.answer = answers("It is in "+home+"/git/hikyaku/internal/cli/cli.go; settings in "+home+"/.config/hikyaku.", 0.1)
	switchAnswerer(t, h, "on")

	ts := h.fake.Say(t, slacktest.DMChannel, "im", owner, "where is the CLI?")
	got := h.postIn(ts, startsWith("It is in"))
	assert.Equal(t, "It is in hikyaku/internal/cli/cli.go; settings in [a local path].", got.Text)
	assert.Contains(t, r.runs()[0], "never by an absolute path")
	assert.Contains(t, r.runs()[0], "Do not answer questions about the owner's personal files")
}
