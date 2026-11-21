# Spec 001: a message there and a reply back

**Issue**: [#1](https://github.com/ushineko/hikyaku/issues/1)

## Status: COMPLETE

## Executive Summary

The headless service (`hikyaku serve --headless`) holds a Socket Mode
connection to Slack and the two tokens, and answers the `hikyaku` CLI over a
private Unix socket: `send`, `ask` (blocking, with `--thread` for follow-ups)
and `inbox`, with replies limited to an allow-list and fenced as data.
Reviewers should look first at `internal/service` (reply routing and the
allow-list), `internal/cli/messages.go` (the reply fence) and
`docs/threat-model.md`. AC9 is open: a reply from a not-allowed account has
not yet been tried in a real channel.

## Context

Long builds and deploys run in coding-agent sessions while the person is
elsewhere. A desktop notification is missed when the screen is locked or the
person is away from the machine; Slack follows them to every device. Slack is
also where the result of a build is passed on to someone else, so a message
formatted for Slack (a link, a status block) is easier to forward than a
terminal scrollback.

The other direction matters as much: an agent that reaches a decision point
("tests pass, deploy to staging?") can ask in Slack and continue on the answer,
instead of idling until the person returns to the terminal.

This spec is the spine: the service, the Slack connection, the local socket and
the CLI an agent calls. It has no window, no setup wizard and no skill. The
setup wizard (spec 002), the window (spec 003) and the agent skills
(spec 004) are built on it.

## Requirements

- R1 `hikyaku serve --headless` connects to Slack in Socket Mode with a bot
  token and an app-level token, and reconnects with backoff when the
  connection drops.
- R2 The service listens on `$XDG_RUNTIME_DIR/hikyaku/hikyaku.sock`, created
  mode 0600 in a 0700 directory. A second `serve` exits with a message
  naming the running one rather than taking its socket.
- R3 `hikyaku send <text>` posts to the user's DM with the bot by default, or
  to `--channel`. It takes text from an argument or stdin, Slack `mrkdwn`
  as-is, and an optional `--title` and `--status ok|warn|fail` that render as a
  header block. It prints the message's handle (an opaque ID mapping to the
  channel and thread timestamp) and exits.
- R4 `hikyaku ask <text> [--timeout D]` posts like `send`, then blocks until a
  reply in that message's thread from an allowed user, prints it, and exits 0.
  On timeout it exits with a distinct code and prints nothing on stdout.
- R5 `hikyaku inbox <handle>` prints the replies received so far in that
  thread, from allowed users, without blocking. `--json` gives a structured
  form for scripts.
- R6 Only Slack user IDs in the settings' `allowed_repliers` can reply to an
  agent. Replies from anyone else are dropped and logged by user ID. With the
  list empty, the default is the user the DM belongs to.
- R7 Output given to an agent marks the reply as text from Slack, with its
  author's ID and time, so it reaches the agent as data, not as instructions.
- R8 Handles and the replies received survive a service restart (a JSON store
  under `$XDG_STATE_HOME/hikyaku/`), so an `inbox` after a restart still works.
  Entries older than a set age are dropped.
- R9 A missing or stopped service makes every CLI command exit non-zero within
  a second, with a message saying to start it.
- R10 Tokens are read from the keyring (D1), put there by
  `hikyaku token set bot|app` from stdin (D3), and never logged, printed or
  included in an error. `hikyaku token status` says which tokens are present
  without showing them.
- R11 `docs/slack-app-manifest.yaml` creates the Slack app with the scopes this
  spec needs and nothing more, and the README says how to install it.
- R12 Every message says where it came from: a line under it naming the
  project (the git repository, else the directory), the branch, the host and
  the agent when it can be detected. The CLI fills it in; `--from <text>`
  replaces it and `--from ""` leaves it out. It carries no path, PID or
  session ID, and it is plain text, so it cannot mention or link anyone.
  Added after the first real-Slack run, which showed that with several
  agents sending, nothing told one message from another.
- R13 A question posted by `ask` says to reply in its thread. Added after the
  first real-Slack run, where a reply typed into the DM instead of the thread
  was (correctly) ignored and the agent never heard it.
- R14 `ask` prints the message's handle on stderr as soon as the question is
  posted, not when it returns, so a question whose `ask` is killed can still
  be read with `inbox`. Found in the first real-Slack run.
- R15 `send` and `ask` take `--thread <handle>` and post in that message's
  thread, in its channel, so one agent's conversation stays in one thread.
  The new message gets its own handle. A reply in a thread holding several
  of hikyaku's messages answers the latest one posted before the reply.
  `--thread` with `--channel` is an error. A follow-up carries no origin
  line and no reply hint unless `--from` is given: the thread's first
  message already says both (asked for in the real-Slack conversation run).

## Acceptance Criteria

- [x] AC1 Against a fake Socket Mode server in the test, the service connects,
  and after the server drops the connection it reconnects with backoff. (R1)
  `slackio.TestTheConnectionComesBackAfterADrop`: a reply is delivered over
  the new connection. The backoff inside a connection is slack-go's; when
  slack-go gives up, `Client.Listen` waits and doubles the wait to five
  minutes. Refused tokens stop the listener instead of retrying
  (`TestRefusedTokensStopTheListener`).
- [x] AC2 The socket and its directory have modes 0600 and 0700; a second
  `serve` exits non-zero and names the first one's PID. (R2)
  `ipc.TestOneServicePrivatelyListening`, `cli.TestASecondServeNamesTheFirst`;
  a directory with looser modes is refused (`TestADirectoryOthersCanEnterIsRefused`),
  and a connection from another uid is closed (`SO_PEERCRED`).
- [x] AC3 `send` posts the expected `chat.postMessage` payload (DM by default,
  `--channel` when given, a header block for `--title`/`--status`) and prints a
  handle. Asserted on the payload at the fake Slack API. (R3)
  `cli.TestSendPostsWhereItIsTold`.
- [x] AC4 `ask` returns the first reply from an allowed user in the thread;
  a reply from a user who is not allowed is ignored; `--timeout` exits with the
  documented code and empty stdout. (R4, R6) `cli.TestAskReturnsTheAllowedReply`,
  `cli.TestAskTimesOutWithItsOwnExitCode` (exit 124).
- [x] AC5 `inbox` returns the replies so far, in order, in both text and
  `--json`, and still returns them after the service restarts. (R5, R8)
  `cli.TestInboxSurvivesARestart`; expiry in `store.TestHandlesExpire`.
- [x] AC6 Reply output carries the author ID, time and a "from Slack" marker. (R7)
  Text and JSON forms, asserted in the ask and inbox tests above. The text
  fence carries a random nonce in both markers, added after the security
  review found a fixed closing marker could be forged from inside a reply
  (`cli.TestAReplyCannotEndItsOwnFence`).
- [x] AC7 With no service running, `send`, `ask` and `inbox` exit non-zero
  in under a second with the documented message. (R9)
  `cli.TestNoServiceIsReportedQuickly` (exit 3, `status` as well).
- [x] AC8 A test feeds tokens through every log and error path and asserts
  neither appears in the output; `token set` stores what stdin gave it, and
  `token status` reports presence without the value. Keyring tests run
  against a fake Secret Service, never the user's own. (R10)
  **Amended**: the fake is go-keyring's in-memory mock (`keyring.MockInit`),
  which replaces the D-Bus provider in the test process; no Secret Service
  is contacted. `secret.TestLogHandlerRedactsEveryAttributeKind`,
  `cli.TestTokensNeverReachOutput` (tokens returned in a Slack error),
  `cli.TestTokenSetAndStatus`.
- [x] AC9 **Integration boundary, real Slack**: with the app installed in a test
  workspace, `send` reaches the DM, `ask` returns a reply typed in Slack, and
  a reply from a second, not-allowed account is ignored. Recorded in the E2E
  Test Plan results, not in CI. (R1, R3, R4, R6)
  **Amended** (owner, 2026-11-20): `send` and `ask` passed against real
  Slack (E2E results, steps 1 and 2). No second account is available, so
  the not-allowed reply is covered by the fake Slack only
  (`cli.TestAskReturnsTheAllowedReply`); the event path it uses is the
  one steps 2 and 5 exercised for real.
- [x] AC10 The manifest creates a working app; its scopes are listed in the
  README with the reason for each. (R11) The README half is held by
  `docs.TestTheManifestAndTheReadmeAgree`; the app created from the manifest
  posted, opened the DM and received thread replies in the real-Slack run
  (E2E results, 2026-11-19).
- [x] AC11 `send` and `ask` post the origin line: the detected project,
  branch, host and agent by default, the `--from` text instead when given,
  and none with `--from ""`. Detection reads the repository's name, not a
  worktree's directory name. Asserted on the payload at the fake Slack API,
  and on the detection against a temporary git repository. (R12)
  `cli.TestEveryMessageSaysWhereItCameFrom` (including that the line is
  plain text), `cli.TestOriginNamesTheRepositoryNotTheWorktree`,
  `slackio.TestTheFooterIsOneShortLine`.
- [x] AC12 A question carries the reply-in-thread line; a `send` does not.
  (R13) `cli.TestEveryMessageSaysWhereItCameFrom`.
- [x] AC13 The handle is on stderr while `ask` is still waiting. (R14)
  `cli.TestAskPrintsTheHandleBeforeTheReply`.
- [x] AC14 A follow-up `ask --thread <handle>` is posted in the first
  message's thread; the reply after it reaches the follow-up and not the
  first question; `inbox` on the first handle does not list it; the
  follow-up has no origin line or hint, but shows an explicit `--from`;
  `--thread` with `--channel`, or with an unknown handle, posts nothing. (R15)
  `cli.TestAConversationStaysInOneThread`,
  `store.TestAReplyAnswersTheLatestMessageBeforeIt`.

## Decisions

- **D1 Tokens live in the Secret Service keyring**, over D-Bus. A 0600 file
  under `$XDG_CONFIG_HOME/hikyaku/` is a fallback only when the user chooses
  it explicitly (`token_store: file` in the settings); hikyaku never falls
  back silently.
  Considered a file by default; rejected because the tokens would sit on disk
  in plaintext and be picked up by backups and dotfile syncs.
- **D2 `github.com/slack-go/slack`**, for its `socketmode` package and the
  Web API calls. Considered writing the websocket and the few API calls
  directly; rejected because reconnect and envelope acknowledgement are
  where the bugs would be, and the library has them already.
- **D3 Setting up the Slack app is spec 002's wizard.** This spec needs only
  a way to put the two tokens in the store: `hikyaku token set bot` and
  `hikyaku token set app` read the token from stdin (never from an
  argument, so it does not reach shell history) and store it.
- **D4 The owner is set by hand until spec 002.** `owner` in
  `settings.yaml` is the Slack member ID whose DM is the default destination
  and, with `allowed_repliers` empty, the one allowed replier. Spec 002's
  pairing writes it.
- **D5 Modules beyond D2**: `github.com/zalando/go-keyring` for the Secret
  Service (over `godbus`, which hayami already uses), `golang.org/x/term` for
  reading a token without echo, and `gorilla/websocket`, already slack-go's,
  for the fake Socket Mode server in tests. Cobra, yaml and testify as in
  hayami.
- **D6 Exit codes**: 0 done, 1 error, 2 usage, 3 no service, 124 `ask` timed
  out (as `timeout(1)`). `ask` prints the handle on stderr so a timed-out
  question can be read later with `inbox`.
- **D7 Replies stay in threads.** A reply typed into the DM, outside any
  thread, is not taken as an answer: the thread is what ties a reply to one
  agent's question when several are waiting. R13 says so on every question.
  Considered taking a DM message as the answer to the latest waiting
  question; rejected because with several agents asking it would answer
  whichever asked last.
- **D8 `ask` is two requests**: post (which returns the handle at once,
  R14) and wait for a reply to that handle. A reply that lands between the
  two is already in the store when the wait begins, so none is missed.
- **D9 The origin is worked out by the CLI**, in the agent's directory, from
  `git` (the common git directory, so a worktree reports its repository),
  the short hostname, and the environment (`CLAUDECODE=1` is Claude Code).
  The service posts what it is given and does not look at the agent.
- **D10 A reply answers the latest message before it** in its thread. Slack
  orders a thread by timestamp, so the rule needs no state beyond the
  store: each message records the thread it was posted in. Considered
  routing by quoting or by mentioning the handle in the reply; rejected
  because it asks the person to type something the thread already says.

## Threat model delta

New document, `docs/threat-model.md`, opened by this spec with these
boundaries:

- **Credentials**: the two Slack tokens. One process holds them.
- **The local socket**: any process running as the user can send and read
  replies. That is the same trust as the user's own shell, and is stated as
  such; other users are kept out by the file modes.
- **What comes back from Slack**: reply text reaches a coding agent. The
  allow-list decides who can speak; the data marker (R7) and the agent-side
  skill (spec 004) decide how it is treated. A compromised allowed Slack
  account can steer the agent; that is stated, not solved here.
- **What is sent to Slack**: an agent may post secrets from its context.
  Spec 004's skill instructs against it; this spec does not filter content.
  The origin line (R12) names the repository, branch and host; in a channel,
  everyone there sees them. `--from` chooses what is shown instead.

The sections are S1 to S4 of `docs/threat-model.md`, with S5 for the supply
chain.

## Risks & Assumptions

- Socket Mode is assumed to be permitted for apps in the target workspace.
- Rollback: the service is a user-level program with no system state beyond
  its socket and state file; stopping it and deleting
  `$XDG_STATE_HOME/hikyaku/` undoes it.
- The JSON store is assumed small (handles expire); it is not a database.

## E2E Test Plan

- Environment: a Slack workspace with the app installed from the manifest,
  the service running headless on Linux.
- Steps: (1) `hikyaku send --title build --status ok "done"`, and the
  message appears in the DM with its header (AC9, R3). (2)
  `hikyaku ask --timeout 5m "continue?"`, a reply typed in the thread on a
  phone, and the command prints it (AC9, R4). (3) A reply from a second
  account in a shared channel is ignored (AC9, R6). (4) Restart the service,
  and `hikyaku inbox <handle>` from step 2 still prints the reply (AC5).

### Results

Real Slack workspace, app created from `docs/slack-app-manifest.yaml`,
service `serve --headless --debug` on Linux, 2026-11-19 and 2026-11-20.

| Step | Result |
| --- | --- |
| 1 `send --title --status ok` with a link | Pass: header, emoji, body and link in the DM |
| 2 `ask`, reply typed in Slack | Pass on the second try. The first reply was typed into the DM, not the thread, and was ignored, as designed; that led to R13 (the reply hint) and D7 |
| 2 `ask` killed while waiting | Found the handle printed only at the end; fixed as R14 |
| 3 Reply from a second, not-allowed account | Not run: no second account. Accepted by the owner as covered by the fake-Slack test (AC9, amended) |
| 4 Restart, then `inbox <handle>` | Pass: the reply was returned after the restart; `status` exited 3 while the service was down |
| 5 Messages that are not replies | Pass: a top-level DM and the bot's own post arrived as events and were not taken as replies |
| 6 Three-turn conversation with `--thread` | Pass: all three questions in one thread, each reply recorded under its own question's handle. Feedback that a follow-up need not repeat the grey line was taken into R15 |
| 7 `ask` timing out | Pass: exit 124, handle and `inbox` hint on stderr |
| 8 Copying a message out of Slack | The origin line is copied with the text, and its two parts ran together ("…Claude Code Reply in this thread…"); the line is now one element joined by " · ". `--from ""` leaves it out of a message meant for copying |

### Prod visibility

Not applicable: a desktop program with no production deployment.
