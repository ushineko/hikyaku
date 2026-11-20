# hikyaku's threat model

What hikyaku defends, against whom, and how, by trust boundary. Opened by
spec 001; a spec that touches a boundary carries a "Threat model delta"
section and updates this document in the same PR.

## Stance

hikyaku runs as the user, on the user's desktop, for the user. It is not a
multi-user service. It trusts the user's own account (anything running as
the user can already read the user's keyring and drive their terminal) and
defends against:

- **other accounts on the machine**, which must not post as the bot, read
  replies or learn the tokens;
- **other people in the Slack workspace**, who can see a channel the bot posts
  to and can write in its threads, and must not steer an agent;
- **leaks through output**: logs, error messages, terminal scrollback and
  pasted bug reports, which must never carry a token.

It does not defend against a compromised user account, a compromised Slack
account on the allow-list, or a compromised Slack.

## Boundaries

| | Boundary | What crosses it |
| --- | --- | --- |
| S1 | Credentials | The bot token and the app-level token |
| S2 | The local socket | Requests from the CLI, replies to it |
| S3 | What comes back from Slack | Reply text, which reaches a coding agent |
| S4 | What goes to Slack | Whatever an agent chooses to post |
| S5 | Supply chain | Go modules, CI Actions, releases |

## S1 Credentials

- The tokens are kept in the Secret Service keyring by default. A 0600 file
  in a 0700 directory is used only when `token_store: file` is set; nothing
  falls back to it silently.
- `hikyaku token set` reads a token from stdin, never from an argument, so it
  does not reach shell history or `/proc/<pid>/cmdline`. At a terminal, input
  is not echoed.
- One process holds the tokens: the service. The CLI never reads them.
- Every log line passes through a redactor that removes the two tokens and
  anything shaped like a Slack token (`xox?-`, `xapp-`). Errors returned to
  the CLI pass through the same redactor. A test feeds tokens through Slack's
  error path and every attribute kind of the logger and asserts neither
  appears.
- **Residual**: the keyring is unlocked for the session; any process running
  as the user can read it. That is the stance above.

## S2 The local socket

- `$XDG_RUNTIME_DIR/hikyaku/hikyaku.sock`, mode 0600, in a directory created
  0700. A directory that exists with another owner or with group or other
  permission bits is refused, not used.
- The service checks the connecting process's user ID (`SO_PEERCRED`) and
  closes a connection from any other account. This is the second lock behind
  the file modes.
- One service per user: a `flock`ed lock file beside the socket. A second
  `serve` exits and names the first one's PID; it never takes over the socket.
- Requests and responses are bounded at one megabyte.
- **Residual**: any process running as the user can post as the bot and read
  replies by handle. That is the same trust as the user's own shell.

## S3 What comes back from Slack

Reply text reaches a coding agent that may act on it, so a reply is treated
as a command for the purpose of deciding who may send one.

- Only Slack user IDs in `allowed_repliers` (or the owner, when the list is
  empty) are heard. A reply from anyone else, in one of hikyaku's threads, is
  dropped and logged by user ID; its text is not stored or logged.
- Only new messages from people, in a thread hikyaku started, count. Edits,
  deletions, bot messages and top-level messages are ignored.
- Reply text is given to the agent fenced and labelled as text from Slack,
  with its author and time, in text and in JSON, so an agent can tell it from
  its own instructions. Each fence carries a random nonce in both markers,
  so reply text cannot close its own fence and write outside it; the text is
  printed unchanged inside. The agent-side skill (spec 004) tells the agent
  how to treat it.
- **Residual**: an allowed account that is compromised can steer the agent.
  The label is a convention for the agent, not an enforcement; prompt
  injection through an allowed reply is possible and is stated, not solved.

## S4 What goes to Slack

- hikyaku posts what the agent gives it, as written. It does not filter
  content.
- Every message carries an origin line: the repository's name, the branch,
  the short host name and the agent. It carries no path, PID or session ID,
  and it is plain text, so it cannot mention anyone or link anywhere. In a
  channel, everyone there sees those names; `--from` replaces the line.
- **Residual**: an agent can post a secret from its context. Spec 004's skill
  instructs against it. The default destination is the owner's DM, which
  limits who sees a mistake.

## S5 Supply chain

- New third-party modules are a decision recorded in a spec (spec 001: D2,
  slack-go; go-keyring for the Secret Service).
- CI Actions are pinned by commit. Dependabot proposes updates weekly, a week
  after release.
- gitleaks runs on every push and pull request.

## Checked

Nothing yet: no adversarial review has been run against this document.
