# Spec 009: roles, and the bot in channels

**Issue**: [#14](https://github.com/ushineko/hikyaku/issues/14)

## Executive Summary

Two roles: admins (a setting, the owner by default) and users, listed at
the desktop with `hikyaku users`. The bot can be put in channels by an
admin, answers @-mentions where they were asked, and leaves when anyone
else adds it; users' runs have no tools and read nothing, an admin's get
the full answerer, and no answer shows a path from the owner's machine.
Reviewers should look first at `answerer.Claude.plainCommand` (the
no-source contract), `Service.channelMessage` and the routing in
`Service.Run`, and the widened exception in `.claude/CLAUDE.md`.

## Status: COMPLETE

## Context

The bot answers one person, its owner, and only in the DM. The owner wants
others to be able to use it, in channels, under roles:

- **Admin** (the owner, for now): invites the bot to a channel with an
  @-mention or asks it to join from the DM (public channels only; a private
  channel has to invite it from inside), and asks it to leave from the
  channel or from the DM. Only an admin's DM is answered by default.
- **User**: listed explicitly. May talk to the bot in a channel it is in.
  DM access is a separate, explicit grant.

Everyone else has no access.

This widens spec 007's exception (unattended runs, owner only) by the
owner's decision of 2026-11-20, under conditions that keep the owner's
credentials away from everyone else: users' runs have no tools and read
nothing. An admin's question gets the full answerer wherever the admin
asks it, channels included; where to ask is the admin's call.

## Threat model (new boundary S7, roles and channels)

### What is at stake

- **Data reach**: the answerer reads GitLab, GitHub, Jira and `~/git` with
  the owner's credentials. A colleague who could start a sourced run could
  read, through the bot, what the owner can and they cannot.
- **Data spread**: an answer in a channel is read by everyone in it.
- **Money**: every run is the owner's usage.
- **The bot's presence**: a channel the bot is in is one whose messages it
  receives.

### Who attacks, and how

1. **A listed user** asks for more than they should get: "read the
   owner's Jira", "ignore your rules", or plants text in a thread the bot
   will read.
2. **Anyone else in a channel** @-mentions the bot or replies in its
   threads.
3. **Anyone in the workspace** invites the bot to a channel, or DMs it.
4. **A compromised admin Slack account**, which could start sourced runs
   and read their answers, in a DM or a channel.
5. **Runaway use** by a user.

### Defences

- **Users' runs can read nothing** (against 1). They are started with no
  tools at all (`--tools ""`), no MCP server, no readable directory, no
  sources in the prompt, and the same flags as spec 007 otherwise. The
  prompt says so; the flags enforce it. What a user can get is the model's
  own knowledge and the text of the thread they are in.
- **Sourced answers reach a channel only when an admin asks there** (data
  spread). Only an admin's question runs the full answerer; its answer is
  scrubbed of secrets and contact details (spec 007, R10) like any other.
  Choosing the channel is the admin's control; there is no review step
  (D4).
- **Roles are an allow-list, set at the desktop** (against 2, 3, 4).
  `admins` is a setting; users are granted with `hikyaku users`, over the
  local socket. Nothing typed in Slack grants or widens access, so a
  hijacked Slack account cannot hand out access.
- **Silence for everyone else** (against 2, 3): a message from someone
  without access starts nothing and gets no reply, so the bot does not
  confirm it is listening; it is logged by user ID, without its text.
- **The bot stays only where an admin put it** (against 3): when someone
  who is not an admin invites it, it leaves at once and tells the admins.
- **Limits** (against 5): $1 per user per day, inside the shared $5 cap.
- Everything from spec 007 still applies to every run: scrubbing, no
  mentions, no unfurling, the label, the queue, the off switch.

### Residual risk

- A user's answer can still be wrong, rude or contain what the model knows
  generally; the label says it is automated.
- Thread text a user's run reads is whatever people wrote in the thread,
  which can include an injection; with no tools, the most it can do is
  shape that answer.
- An admin's sourced answer in a channel is read by everyone there, without
  review: whatever the allow-list reaches and the scrubber does not remove,
  such as a ticket's text or a private repository's commit messages, can
  land in a channel the admin asked in.
- A compromised admin account can start sourced runs, in a DM or a channel,
  as with spec 007's threat 2; the off switch still applies.

## Requirements

- R1 **Roles.** `admins` in the settings lists admins' Slack IDs; empty
  means the owner. Users are kept in `access.json` in the state directory
  and managed with `hikyaku users list`, `hikyaku users add <id> [--dm]`,
  `hikyaku users remove <id>` and `hikyaku users dm <id> on|off`, which the
  service applies at once and logs. An admin is never a user entry.
- R2 **Who is answered where.**
  - DM: an admin's top-level DM goes as today (agents, then the answerer,
    with the full allow-list). A user with the DM grant gets a no-source
    answer in the DM. Anyone else's DM gets nothing.
  - Channel (one the bot is in): a message that @-mentions the bot, from an
    admin or a user, is a question, top-level or in any thread. Nothing
    else is: a reply in a thread, even one the bot answered in, needs its
    own mention (owner, 2026-11-20: "it shouldn't respond on a thread
    unless it was instructed to"), with one exception: in the thread under
    the bot's own top-level answer, the person who asked may reply without
    one (owner, same day: "the asker can reply without a mention if the bot
    was the top message"). Anyone else gets nothing.
  - **Answer where it was asked** (owner, same day): a top-level mention
    gets a top-level answer in the channel; a mention in a thread gets an
    answer in that thread, with the thread's earlier messages as context.
    The bot's progress shows as reactions on the asking message (👀 while
    it works, ✅ when answered), not as notes; a note that has to say
    something (a limit, a failure) goes in the message's thread.
- R3 **No-source runs** (users, in channels and DMs): started with spec
  007's flags but `--tools ""`, no `--allowedTools`, no `--mcp-config`, no
  `--add-dir`; the prompt says the run has no access to any system, answers
  from general knowledge and the thread, and that the asker is not the
  owner. The persona's voice applies. The thread's earlier messages, when
  the question is in a thread, are given fenced as data.
- R4 **An admin's question gets the full answerer, anywhere.** In a DM or
  a channel, it runs with spec 007's allow-list, and the answer is posted
  where it was asked, scrubbed and labelled as usual, with no draft or
  approval (owner, 2026-11-20, D4).
- R5 **Join and leave.**
  - An admin's DM `join <#channel>` joins a public channel and says so; for
    a private one it answers "I can't join a private channel myself; invite
    me from inside it." `leave <#channel>` leaves it.
  - In a channel, an admin's `@bot leave` makes it leave; a user's gets
    "Only an admin can ask me to leave." (users may talk to it there).
  - When the bot is added to a channel by someone who is not an admin, it
    leaves at once and DMs each admin who invited it and where.
  - `join` and `leave` in a DM are commands only when the message is
    exactly that; otherwise it is a question as before.
- R6 **Limits.** Each user's runs are capped at $1 a day; all runs share
  spec 007's $5. A user over their cap gets "That's my limit for you today."
  `hikyaku status` shows users' spending with today's total.
- R7 **Scopes.** The manifest adds `channels:join`, `channels:manage`,
  `groups:write`, `channels:read`, `groups:read` and the
  `member_joined_channel` event; the README's scope table says why. Without
  them, join/leave answer with what scope is missing.
- R8 `.claude/CLAUDE.md`'s exception records the widening: who (admins;
  users with no-tool runs), and the conditions (R3, R4, the desktop-only
  grant).

## Acceptance Criteria

- [x] AC1 Against the fake Slack and a fake runner: in a channel, an
  admin's and a listed user's @-mention start a run; a stranger's, and a
  message without a mention, start nothing and get no reply. A top-level
  mention is answered top-level, with 👀 then ✅ on it and no note; in the
  thread under that answer, the asker's reply without a mention is
  answered there, anyone else's is not; a reply without a mention in any
  other thread starts nothing; a mention in a thread is answered in it,
  with the thread's earlier messages in the prompt. (R2)
  `cli.TestAChannelHearsOnlyMentionsFromPeopleWithAccess`.
- [x] AC2 DMs: a user without the grant gets nothing; with `users dm <id>
  on`, a no-source answer; after `users remove`, nothing. An admin's DM is
  unchanged. (R1, R2)
  `cli.TestDMsNeedTheGrant`.
- [x] AC3 A user's run is started with `--tools ""`, without
  `--allowedTools`, `--mcp-config` or `--add-dir`, and its prompt says it
  has no access and the asker is not the owner; an admin's DM run keeps the
  full flags. (R3) This is the security contract, tested as one.
  `answerer.TestAPlainRunCanReadNothing`, `answerer.TestAPlainPromptSaysWhoIsAsking`; through the service, a separate fake runner for plain runs shows which runner each question went to.
- [x] AC4 An admin's channel question runs the full answerer and is
  answered where it was asked, with ✅ on it, and nothing goes to a DM.
  (R4)
  `cli.TestAnAdminsChannelQuestionIsAnsweredDirectly`.
- [x] AC5 `join <#C…>` from an admin's DM joins; a private channel gets the
  invite-from-inside answer; `leave` from the DM and an admin's `@bot
  leave` leave; a user's `@bot leave` gets the refusal; a non-admin's
  invite makes the bot leave and DM the admin. (R5)
  `cli.TestAdminsDecideWhereTheBotIs`.
- [x] AC6 A user over $1 today gets the limit answer and starts no run;
  the shared cap still applies; `status` shows users' spending. (R6)
  `cli.TestAUsersDayIsCapped`.
- [x] AC7 `users add|remove|dm|list` change access at once and survive a
  restart; nothing typed in Slack changes access. (R1)
  `cli.TestAccessChangesOnlyAtTheDesk`.
- [x] AC8 The manifest and README scopes agree (the docs test). (R7)
  `docs.TestTheManifestAndTheReadmeAgree`.
- [x] AC9 **Real Claude Code**: a no-source run asked to read a file, run a
  command and fetch a URL does none of them, and says it has no access.
  (R3, threat model)
  `answerer.TestAPlainRunReadsNothing` (e2e).
- [x] AC10 **Real Slack**, after the reinstall: the admin invites the bot
  to a test channel; the admin's @-mention is answered in the channel; in
  the thread under that answer, the admin's reply without a mention is
  answered; the admin's `leave` from the DM works. A listed user's
  @-mention gets a no-source answer, if a second account is available;
  without one, the no-source path is held by AC1 against the fake and AC9
  against real Claude Code. (R2 to R5)
  See E2E results.

## Decisions

- **D1 Users' runs read nothing** (owner, 2026-11-20). Anything readable
  with the owner's credentials is readable by whoever can start a run.
  Considered a smaller allow-list for users; rejected because it is still
  the owner's credentials.
- **D2 Listed users only** (owner, 2026-11-20), rather than everyone in a
  channel the bot is in: channels change membership; a list does not.
- **D3 Access is granted at the desktop only** (owner, 2026-11-20).
- **D4 No approval step** (owner, 2026-11-20). First decided as drafts in
  the admin's DM, approved by reply or reaction before reaching the
  channel; built, tried in real Slack, and removed the same day: "lets get
  rid of the approval workflow for now. i don't like it. but users should
  still be bound by 1 (minimum perms unless granted)." Admins' answers are
  posted where they asked; users' runs still read nothing (D1).
- **D5 $1 per user per day inside the shared $5** (owner, 2026-11-20).
- **D6 @-mention in a channel; silence for others; leave on a non-admin's
  invite** (2026-11-20, stated to the owner when drafting). A bot that
  answered every message in a channel would be noise and would read
  everything; a reply to people without access would confirm it is there.
- **D7 Mentions are read from message events**, by the bot's own user ID
  (learnt with `auth.test`, which needs no scope), rather than the
  `app_mention` event, so no `app_mentions:read` scope is needed.
- **D8 Agents stay in the DM.** `listen` hears only DMs, as in spec 005.
- **D9 Per-user source grants come later** (owner, 2026-11-20: "allow
  specific grants / rescinding of perms by an admin ... ask a question of
  jira, or ... of a specific project"). Spec 010. Each user is an entry in
  `access.json` so grants can be added to it. A grant is still the owner's
  credentials; "Jira" can be granted as the Atlassian read tools, but "one
  project" cannot be enforced with prefix rules alone (`-R other/project`
  passes a rule written for one project), so 010 needs a fixed-project
  wrapper or a project-scoped token, verified against real Claude Code.
  The point of a project grant (owner): "to avoid questions accidentally
  pulling in information in ~/git that's part of personal or non-work
  projects". For local repositories that is enforceable: file rules match
  paths (`Read(~/git/<repo>/**)`), and `--add-dir` can name the repository
  alone. The same could limit the admin's own channel answers to work
  repositories, which matters more without an approval step.

## Also in this change

- **An app icon** (owner, 2026-11-20, asked during this work: "it also
  needs an icon. something thematically related to SOUL is probably
  suitable"): a hitodama, the soul-flame of Japanese folklore, carrying a
  sealed letter, the courier. `docs/img/icon.svg` and a 1024 px
  `icon.png`, uploaded in the app's settings; a manifest cannot set it.
- **No paths from the machine in an answer** (owner, same day: "it
  includes answers with the full paths from my machine ... those should be
  anonymized/generalized", and "don't quote file paths from outside the
  areas either, you can talk about them generically if they are setup
  files ... but don't answer questions about personal files"). The prompt
  asks for repository-relative paths, generic names for setup files, and a
  refusal for personal files outside the repositories. Every answer then
  passes `answerer.Paths.Generalize`: a path under `~/git` becomes
  repository-relative, any other path on the machine (`~/…`, `/home/…`,
  `/etc/…`, `/tmp/…` and other system folders) becomes `[a local path]`;
  URLs, API routes and prose are left alone. Tested in
  `TestAnAnswerShowsNoPathsFromTheMachine` (unit and through the service).
- **Links into source control** (owner, same day: "when asking about
  specs/tickets, would be good if it is allowed to get links into source
  control ... admin only"). The allow-list gains `git remote get-url`,
  exactly `git remote -v`, `gh repo view` and `glab repo view`, all
  read-only (git refuses anything but a name after `get-url`; checked), and
  the prompt asks for a link whenever an answer names a spec, file, MR or
  PR, commit or ticket. Users' runs have no tools, so this is admin-only.
  Real Claude Code denied `git remote add`, `git remote -v add` and
  `git remote set-url` and read the address (`TestThePermissionCheckHolds`).
  Before this, a run tried to read `.git/config` for the address and was
  rightly denied. In the first real use, the run chained its commands
  (`cd … && git remote get-url …`), which the permission check denies as a
  whole, and then tried `git -C`, which no rule matches; the prompt now asks
  for one command per call, `cd` first, and never `git -C`, and allows
  exactly `git branch --show-current`. Real Claude Code then linked
  `specs/007-…` on GitHub with nothing denied, three runs in three
  (`TestAnAnswerLinksWhatItNames`).
- **References picked at random** (owner, same day: "the flavor comments
  ... always refer to hikyaku instead of randomly choosing from our pool").
  The prompt no longer calls the run "hikyaku's answerer", and a persona
  may list `## References`; each answer's voice names one, picked at random
  (`persona.VoiceForAnswer`, `TestEachAnswerDrawsItsReferencesFromOneSource`).
- **The persona's emoji guidance** (owner, same day): an emoji that fits a
  reference ("the larch" 🌲), varied, not defaulting to sea creatures.

## Risks & Assumptions

- Five scopes and an event need one more reinstall; until then, channel
  features say what is missing and everything else works.
- The bot receives every message in a channel it is in (it must, to see
  mentions); it keeps none of them except its own threads' exchanges, in
  memory, as today.
- Rollback: `hikyaku answerer off` stops all runs; removing users and
  leaving channels undoes access; revert for the code.

## E2E Test Plan

- Environment: the app reinstalled with the new scopes; a test channel; a
  second person listed as a user, if one is available.
- Steps:
  1. The admin invites the bot to the test channel by @-mention (AC10,
     R5).
  2. A listed user @-mentions the bot with a question; a no-source answer
     appears in the thread (AC10, R2, R3).
  3. The admin @-mentions the bot with a sourced question; the answer
     appears in the channel with ✅ on the question; the admin replies under
     it without a mention and gets an answer in the thread (AC10, R2, R4).
  4. The admin DMs `leave <#channel>`; the bot leaves (AC10, R5).
  5. `go test -tags e2e -run PlainRunReads ./internal/answerer/` (AC9).

### E2E results (2026-11-20, real Slack, real Claude Code)

The app was recreated from the manifest as `nykbot-α` (Slack made the
username `nykbota`) with all the scopes; the service ran headless from the
branch; there was no second account, so the user path is held by AC1
against the fake and AC9 against real Claude Code.

| Step | Result |
| --- | --- |
| 1 Invite | Pass. The admin invited the bot to a private test channel; it logged an admin's invite and stayed |
| 2 Listed user | Not run: no second account (see above) |
| 3 Admin question | Pass. First as drafts with approval (by reply and by ✅ reaction), which the owner then removed (D4); after that, answered directly in the channel with 👀 then ✅. A reply under the answer without a mention was answered in the thread; one without a mention elsewhere was not |
| 4 Leave | Pass. `leave #channel` from the DM: "an admin asked the bot to leave a channel", ok |
| 5 AC9 | Pass, `TestAPlainRunReadsNothing` |

Found and fixed during the test, each now in the spec: answers in a
thread rather than where asked; notes neutralizing the bot's own channel
links; machine paths in answers; references always about hikyaku; links
blocked by chained commands and `git -C`.

### Prod visibility

Not applicable: a desktop program with no production deployment.
