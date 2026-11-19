# Spec 002: a setup that walks you through Slack

**Issue**: [#2](https://github.com/ushineko/hikyaku/issues/2)

## Status: INCOMPLETE

## Context

A Slack app that talks to one person is set up by hand at api.slack.com:
create the app, choose scopes, install it, copy a token, store it somewhere.
`ag-scripts/slack-presence-toggle` documents that path in its README, and it
is the path that gets forgotten between one machine and the next.

hikyaku needs more than that tool did: two tokens (the bot token, `xoxb-`, and
an app-level token, `xapp-`, for Socket Mode), Socket Mode switched on, an
event subscription, and the person's own Slack user ID, which is the default
destination and the first entry of the reply allow-list.

Slack accepts a manifest in the URL that opens its app-creation page
(`https://api.slack.com/apps?new_app=1&manifest_yaml=<url-encoded>`), so the
scopes, the events and Socket Mode need no clicking: the person chooses a
workspace and confirms. What cannot be automated without a configuration
token is the two copy-and-paste steps, and those are what the wizard is
for.

## Requirements

- R1 `hikyaku setup` runs the steps in the terminal; the window (spec 003)
  runs the same steps through the fynedesygn wizard (`docs/wizard.md` in that
  repository). The steps live in core and neither shell decides what they are.
- R2 Step 1 opens the app-creation URL built from
  `docs/slack-app-manifest.yaml` (spec 001, R11), with the URL also printed for
  a machine without a browser.
- R3 Step 2 asks for the bot token and step 3 for the app-level token
  (Basic Information → App-Level Tokens, scope `connections:write`). Each
  step says exactly where on the Slack page the token is. Input is not
  echoed, and a value is checked for its prefix before anything is sent.
- R4 Each token is verified before it is stored: `auth.test` for the bot
  token, `apps.connections.open` for the app-level token. A failure says
  which token and what Slack answered, without the token.
- R5 Pairing: the wizard connects in Socket Mode, shows a short one-time
  code, and asks the person to DM it to the bot. The first DM carrying the
  code sets the person's user ID as the default destination and the first
  allowed replier. A DM without the code is ignored, so someone else who DMs
  the bot first does not become its owner. The code expires after ten
  minutes.
- R6 The last step sends a test message to the person's DM and says setup is
  done.
- R7 Running setup again on a configured machine says what is configured
  and offers to replace the tokens, re-pair, or stop. It never clears a
  working configuration before the new one is verified.
- R8 `hikyaku setup --reset` removes the tokens from the keyring and the
  pairing from the settings, after confirmation.

## Acceptance Criteria

- [ ] AC1 The setup steps are one list in core; a test runs them with a
  scripted terminal and a fake Slack. (R1)
- [ ] AC2 The URL decodes back to the committed manifest byte for byte. (R2)
- [ ] AC3 A token with the wrong prefix is refused without a network call; a
  token Slack rejects is not stored; neither token appears in any output.
  (R3, R4)
- [ ] AC4 Pairing: a DM with the wrong code is ignored, the right code pairs,
  and an expired code no longer pairs. (R5)
- [ ] AC5 Re-running setup with a new, invalid token leaves the old,
  working configuration in place. (R7)
- [ ] AC6 `--reset` removes the tokens and the pairing; `hikyaku token status`
  then reports none. (R8)
- [ ] AC7 **Integration boundary, real Slack**: on a machine without a
  configuration, `hikyaku setup` goes from nothing to a test message in the
  DM, and the steps' directions match what the Slack pages actually show.
  Recorded in the E2E Test Plan results. (R2–R6)

## Threat model delta

- **Credentials**: the tokens pass through the wizard's input once and go
  to the keyring. No file, no argument, no environment variable.
- **Pairing** is the moment the bot learns whom to trust. The one-time code
  and its expiry are what keep a second person in the workspace from
  claiming it.

## Risks & Assumptions

- The manifest URL is a documented Slack link pattern, but Slack can change
  its app pages; the directions in R3 are text that may need updating.
- A workspace that requires admin approval for new apps stops at step 1
  until it is approved. The wizard says so rather than failing.
- Rollback: `hikyaku setup --reset`, and delete the app at api.slack.com.

## E2E Test Plan

- Environment: Linux desktop, a Slack workspace where the person can install
  apps, no hikyaku configuration present.
- Steps: run `hikyaku setup`, follow each step, pair from the Slack client,
  and see the test message arrive (AC7). Re-run with a deliberately wrong bot
  token and see the old configuration still send (AC5).

### Prod visibility

Not applicable: a desktop program with no production deployment.
