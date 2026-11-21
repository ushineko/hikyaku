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
account on the allow-list, or a compromised Slack. With the answerer on
(S6), a compromised owner Slack account reaches further than an answer to
an agent; S6 states how far.

## Boundaries

| | Boundary | What crosses it |
| --- | --- | --- |
| S1 | Credentials | The bot token and the app-level token |
| S2 | The local socket | Requests from the CLI, replies to it |
| S3 | What comes back from Slack | Reply text, which reaches a coding agent |
| S4 | What goes to Slack | Whatever an agent chooses to post |
| S5 | Supply chain | Go modules, CI Actions, releases |
| S6 | The answerer | The owner's question, into a Claude Code run; its answer, back to Slack |

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
- Instructions (spec 005) are the one way Slack text reaches an agent that
  did not ask a question: an agent running `hikyaku listen` receives a
  top-level message from an allowed user in their DM with the bot. Messages
  in channels are never instructions, even from an allowed user, so channel
  topics and other people's text cannot reach a listener. Nothing listens
  unless an agent chose to, one `listen` takes one instruction, and an
  instruction nobody takes is answered and dropped, never kept for later.
  The text is fenced with a nonce and labelled as typed in Slack.
- hikyaku starts no agent except in one mode, the answerer (S6), which is
  off until the owner switches it on at the desktop, starts only read-only
  runs, and answers only the owner. A chat message that can start work on
  the desktop is the design that made other assistants' Slack integrations
  an attack path (OpenClaw, CVE-2026-24764); S6 is how this project keeps
  that mode narrow.
- **Residual**: an allowed account that is compromised can steer the agent,
  and with spec 005 can direct a listening agent, not only answer one. The
  label is a convention for the agent, not an enforcement; prompt injection
  through an allowed reply or instruction is possible and is stated, not
  solved. The agent-side skill (spec 004) tells an agent to confirm anything
  destructive before acting on an instruction.

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

## S6 The answerer

Spec 007. With the answerer on, a top-level DM from the owner that no
listener takes starts a fresh Claude Code run (`claude -p`), and its answer
is posted in the message's thread. This reverses the rule that hikyaku
starts no agents, for this mode only, as an experiment the owner authorized
(recorded in `.claude/CLAUDE.md`).

**What is at stake.** The run acts as the owner, with the owner's
credentials for the sources it reads (GitLab, GitHub, Jira, local
repositories). So are changes made as the owner, data leaving to somewhere
other than the owner's DM, model spending, and the owner's trust in the
answer.

**Who attacks.**

1. **Text inside the sources**, the main threat: an MR description, issue
   comment, commit message, CI log or file saying "run this", "fetch this
   URL". This is prompt injection, and the model cannot be relied on to
   resist it.
2. **A compromised owner Slack account**, which can start runs and read
   their answers.
3. **Anyone else in the workspace**, who can DM the bot.
4. **Runaway use**: a flood of messages, a run that does not end.

**Defences.** The run is limited by what it can do, not by what it is told.

- It is started with `--permission-mode dontAsk` and `--permission-prompts
  none`: anything not on the allow-list is denied, with no prompt and no
  one to answer one. `--setting-sources ""` loads none of the owner's
  Claude Code settings, so no allow rule of theirs widens the list.
- `--tools Bash,Read,Grep,Glob` makes these the only built-in tools in
  the run, and `--disable-slash-commands` turns off skills. Without this,
  tools that need no permission (messaging other Claude sessions,
  notifications, remote triggers, scheduled tasks, subagents) would be
  there whatever the allow-list says; this was found by listing a run's
  tools, not assumed.
- The allow-list names read-only commands by prefix (`glab mr view`, never
  `glab api` or `glab mr merge`) and file reads under `~/git`. It holds no
  tool that writes, edits, pushes or posts, and no `WebFetch`, `WebSearch`
  or `curl`. MCP servers come only from `answerer.mcp_config`
  (`--strict-mcp-config`), and only their read tools are allowed. The run
  is started with `READ_ONLY_MODE=true`, so an MCP server that knows it
  (the Atlassian one does) does not even offer its write tools. A test
  checks the list holds nothing that writes or fetches.
- Files that hold credentials stay unread, whatever the allow-list says:
  deny rules (`--disallowedTools`) cover `Read` and `Grep` of `.env*`,
  `*.pem`, `*.key`, SSH keys, `.netrc`, `credentials*`, `.git/config`,
  Terraform state and similar under `~/git`. A deny rule wins over an
  allow rule, and Claude Code also drops denied files from a folder
  search; both were checked against real Claude Code with planted files.
- Every answer is scrubbed before it is posted: shapes of credentials
  (private keys, AWS, GitHub, GitLab, Slack, Atlassian, Anthropic and
  Google tokens, JWTs, passwords in URLs, `password=`/`token:`-style
  assignments) become `[redacted]`, and the answer says something was
  removed; email addresses and phone numbers become `[email]` and
  `[phone]`. This catches what comes through an allowed route, such as a
  key in a commit shown by `git show` or a token pasted in an issue. A
  follow-up's prompt holds the scrubbed answer, not the original. The log
  counts what was removed, never what it was.
- The run works in an empty directory of its own and in `~/git`
  (`--add-dir`), so it can `cd` into a repository for `git log`. In both,
  the allow-list decides what it may run.
- With no network tool, the only way out is the answer, and it goes only to
  the owner's DM. It is posted with link unfurling off, so Slack does not
  fetch a URL in it, and with special mentions shown as text.
- Nothing persists between runs: `--no-session-persistence`, an empty
  working directory per run, removed after it, and no write tools.
- The run's environment holds no hikyaku variable, nothing the redactor
  recognizes as a Slack token, and none of the variables of a Claude Code
  session the service may have been started from, which carry a way to
  message other sessions (only `CLAUDE_CONFIG_DIR` is kept). Ending a run kills its process group, so the
  tools it started end with it.
- Only the owner starts runs; `allowed_repliers` can answer agents but
  cannot. Anyone else gets silence, as in S3.
- Off by default. `hikyaku answerer on|off` switches it over the local
  socket (S2); nothing from Slack can. `off` ends a running run.
- Limits: one run at a time, three waiting, five minutes and $0.50 a run
  (`--max-budget-usd`), $5 a day.
- The answer is labelled as automated and possibly wrong. The question is
  fenced with a nonce in the prompt, and the prompt says text in sources is
  data. That is a convention for the model; the defences above do not rely
  on it.
- Each run is logged: duration, cost, how it ended, and each denied tool
  call by its tool and a shortened, scrubbed form of its input. The
  question and the answer are not logged. A run that reports no cost
  (ended by its time limit or by `off`, or broken) is counted at its whole
  budget, so the day's total does not run low.

**Residual risk.**

- An injected source can make an answer false or misleading. The label
  says so.
- Everything the allowed tools can read can reach the owner's DM. That is
  the purpose. It is also what a compromised owner Slack account gets while
  the answerer is on: read access, through the owner's credentials, to what
  the allow-list reaches. The off switch and a short allow-list limit it.
  It is not solved.
- The allow-list depends on Claude Code's permission matching, including
  how it splits compound shell commands. The e2e tests (spec 007, AC9)
  attack this against real Claude Code, asking for each forbidden action
  directly so the attempt reaches the permission check rather than
  stopping at the model's judgement: chained (`;`, `&&`), substituted and
  redirected commands, unlisted subcommands (`glab api`, `git push`),
  `curl`, `touch`, and reading outside `~/git`. All were denied with
  Claude Code 2.1.312. It is rerun after a Claude Code
  upgrade; the matching is Claude Code's, not hikyaku's.
- The scrubber works on known shapes. A credential with no recognizable
  shape (a bare random password in prose) or personal data other than
  email addresses and phone numbers (names, addresses) is not removed.
  The deny rules cover files by name; a credential in a file with an
  ordinary name is readable, and only the scrubber stands between it and
  the DM.
- The runs use the owner's Claude Code login, with no spending limit at the
  account level; hikyaku's caps are the only limit.
- A link in an answer can still be clicked by the owner.

## Checked

Nothing yet: no adversarial review has been run against this document.
