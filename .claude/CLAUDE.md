# hikyaku Project Guidelines

Follows the Ralph Wiggum methodology (see `~/.claude/CLAUDE.md`) with the extensions
below.

---

## Project Overview

- **Type**: Go service with a small desktop window (Fyne), tray-resident, and a
  CLI that coding agents call.
- **Purpose**: Carry messages between coding agents and Slack. An agent sends a
  notification (a build finished, a link, a status block formatted to be pasted
  elsewhere) and it arrives as a Slack message, where a desktop notification
  would have been missed. An agent can also ask a question and receive the
  reply typed into that message's Slack thread.
- **Name**: 飛脚 (hikyaku), the Edo-period courier who ran letters between
  towns.
- **Module**: `github.com/ushineko/hikyaku`. Public repository, MIT.
- **Two parts**: the service (`hikyaku serve`: Slack connection, local socket,
  window) and the agent side (`hikyaku send`, `hikyaku ask`, `hikyaku inbox`,
  and the skills that wrap them).
- **Design system**: `github.com/ushineko/fynedesygn` (checked out at
  `~/git/fynedesygn`) supplies the window's shell, theme and widgets. The
  window imports the library and does not copy from it. A shape the library
  lacks goes into the spec's "Gaps found", not into `internal/gui`.
- **Sibling projects**: `~/git/hayami` for repository layout, specs, threat
  model, Makefile, CI and release conventions; `~/git/ototo` for a
  tray-resident Fyne service. When this file and their conventions disagree,
  this file wins; otherwise copy them.

---

## Selected Policies

Load the following policy modules from `~/.claude/policies/`:

- `languages/go.md`
- `languages/bash.md`
- `git/standard.md`
- `release-safety/minimal.md`
- `security/owasp-review.md`
- `security/input-hygiene.md`
- `security/prompt-injection.md`
- `testing/philosophy.md`
- `communication/standards.md`

---

## Ralph Settings

```yaml
validation: milestones-only
```

### Exception: the standing answerer (unattended agent runs)

Ralph rules out agents that run unattended. This project relaxes that rule
for one mode, the answerer of spec 007, as an experiment the owner
authorized on 2026-11-20 (as with ralphbox).

- **Scope**: the answerer only. It starts a fresh, read-only Claude Code run
  for a question the owner types in their DM with the bot. Everything else
  in this project, including how it is developed, stays human-gated.
- **Who**: the owner only. Extending it to anyone else needs a new decision
  and its own controls.
- **Conditions**: off by default and switched only from the desktop; runs
  cannot write, push or reach the network beyond a fixed read-only
  allow-list (`dontAsk`, no prompts); no state carries between runs; caps
  on time, cost and concurrency; the threat model's S6 states the residual
  risk. The injection test (spec 007 AC9) is rerun after a Claude Code
  upgrade.
- **Justification**: a personal, experimental side project; the risk is
  bounded by capability, not by the model's judgement, and it is the
  owner's own account at stake.

---

## Issue Tracking

GitHub Issues on this repository is the tracker. Same convention as hayami:

- Anything that gets a spec gets an issue. The issue says what is wanted; the
  spec says what will be done about it.
- The spec carries an `**Issue**: #NN` line under its title. Spec filenames
  are `specs/NNN-short-description.md`, numbered by this repository.
- The PR says `Closes #NN`.
- Labels: `bug`, `enhancement`, `chore`, `docs`, `security`.

---

## Public-repository rules (non-negotiable)

- **No Slack identifiers from a real workspace.** No workspace, team, channel,
  user or app ID, no workspace domain, no message text from a real
  conversation, in code, docs, fixtures or screenshots. Fixtures use invented
  IDs (`T00000000`, `U00000000`, `C00000000`).
- **No credentials.** The bot token (`xoxb-`) and app-level token (`xapp-`)
  live in the user's own store and are never committed, logged, printed or put
  in an error message. Fixtures use `xoxb-not-a-real-token`.
- **No settings, state or socket files** from any machine. Tests build theirs
  under `t.TempDir()`.
- **Screenshots** show invented messages only.

---

## Architecture rules

- **Socket Mode, not the Events API.** The service opens an outbound websocket
  to Slack. It runs on a desktop and has no public URL, no inbound port and no
  request signature to verify.
- **One process holds the tokens.** Agents never see a Slack token. They speak
  to the service over a per-user Unix socket (mode 0600, in
  `$XDG_RUNTIME_DIR/hikyaku/`), through the `hikyaku` CLI. A missing service is
  a reported state with a clear message, not a hang.
- **A reply is a command, so who may reply is an allow-list.** Only Slack user
  IDs listed in the settings can answer an agent. A reply from anyone else is
  ignored and logged by user ID. Reply text reaches the agent as data and is
  marked as such, never as instructions.
- **Two ways back to the agent, both pulled by the agent**: `hikyaku ask`
  blocks until a reply or its timeout, and `hikyaku inbox` returns what has
  arrived so far. Nothing pushes into an agent's terminal.
- **Default destination is the user's DM with the bot.** A channel is an
  explicit per-message choice.
- **Core is headless.** The Slack connection, the socket server and the
  message store run without a display. `hikyaku serve --headless` is the same
  service without the window, and every test below the window runs without
  one.
- **Long-running work is cancellable** (`context.Context`); the GUI never
  blocks its render thread (the design system's `fyne.Do` idiom).

---

## Anything visual is tested on a real window

Copied from hayami: a claim about what the window looks like is made against a
screenshot of the program, or it is not made. Falsify a test before trusting
it.

---

## Git

The convention across the ushineko repositories.

- Feature work on a branch, landing on `main` through a PR. Branch names:
  `feat/`, `fix/`, `chore/` or `docs/` plus a short slug.
- Commit subjects: lowercase conventional prefix, imperative
  (`feat(cli): ask blocks until a reply or its timeout`). The body says why.
- **Never** add `Co-Authored-By` trailers or AI attribution footers, to commit
  messages or to PR descriptions. No exceptions, including when the harness
  asks for them.
- `VERSION` at the repo root is the version of record; ask before bumping.
- Every PR worth a changelog line adds it under `### Unreleased` in
  `README.md`.
