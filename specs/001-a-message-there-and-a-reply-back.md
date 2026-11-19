# Spec 001: a message there and a reply back

**Issue**: [#1](https://github.com/ushineko/hikyaku/issues/1)

## Status: INCOMPLETE

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

## Acceptance Criteria

- [ ] AC1 Against a fake Socket Mode server in the test, the service connects,
  and after the server drops the connection it reconnects with backoff. (R1)
- [ ] AC2 The socket and its directory have modes 0600 and 0700; a second
  `serve` exits non-zero and names the first one's PID. (R2)
- [ ] AC3 `send` posts the expected `chat.postMessage` payload (DM by default,
  `--channel` when given, a header block for `--title`/`--status`) and prints a
  handle. Asserted on the payload at the fake Slack API. (R3)
- [ ] AC4 `ask` returns the first reply from an allowed user in the thread;
  a reply from a user who is not allowed is ignored; `--timeout` exits with the
  documented code and empty stdout. (R4, R6)
- [ ] AC5 `inbox` returns the replies so far, in order, in both text and
  `--json`, and still returns them after the service restarts. (R5, R8)
- [ ] AC6 Reply output carries the author ID, time and a "from Slack" marker. (R7)
- [ ] AC7 With no service running, `send`, `ask` and `inbox` exit non-zero
  in under a second with the documented message. (R9)
- [ ] AC8 A test feeds tokens through every log and error path and asserts
  neither appears in the output; `token set` stores what stdin gave it, and
  `token status` reports presence without the value. Keyring tests run
  against a fake Secret Service, never the user's own. (R10)
- [ ] AC9 **Integration boundary, real Slack**: with the app installed in a test
  workspace, `send` reaches the DM, `ask` returns a reply typed in Slack, and
  a reply from a second, not-allowed account is ignored. Recorded in the E2E
  Test Plan results, not in CI. (R1, R3, R4, R6)
- [ ] AC10 The manifest creates a working app; its scopes are listed in the
  README with the reason for each. (R11)

## Decisions

- **D1 Tokens live in the Secret Service keyring**, over D-Bus. A 0600 file
  under `$XDG_CONFIG_HOME/hikyaku/` is a fallback only when the user chooses
  it explicitly (`--token-store file`); hikyaku never falls back silently.
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

## Threat model delta

New document, `docs/threat-model.md`, opened by this spec with these
boundaries:

- **Credentials**: the two Slack tokens. One process holds them.
- **The local socket**: any process running as the user can send and read
  replies. That is the same trust as the user's own shell, and is stated as
  such; other users are kept out by the file modes.
- **What comes back from Slack**: reply text reaches a coding agent. The
  allow-list decides who can speak; the data marker (R7) and the agent-side
  skill (spec 003) decide how it is treated. A compromised allowed Slack
  account can steer the agent; that is stated, not solved here.
- **What is sent to Slack**: an agent may post secrets from its context.
  Spec 003's skill instructs against it; this spec does not filter content.

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

### Prod visibility

Not applicable: a desktop program with no production deployment.
