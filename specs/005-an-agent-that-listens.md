# Spec 005: an agent that listens

**Issue**: [#4](https://github.com/ushineko/hikyaku/issues/4)

## Status: COMPLETE

## Executive Summary

Adds `hikyaku listen`: a running agent waits for one instruction typed as
a top-level message in the DM with the bot, receives it fenced and
labelled as Slack data, and answers in its thread. Instructions are
addressed by listener name when several agents listen; one nobody takes
is dropped and the bot says so. Reviewers should look first at
`internal/service/listen.go` (allow-list check, routing, hand-over under
the lock) and the S3 changes in `docs/threat-model.md`.

## Context

Spec 001 carries one direction of conversation that the agent starts: it
sends, or it asks and waits in a thread. A message typed into the DM with
the bot outside any thread reaches the service and is ignored.

The wanted use is the other way round: a person away from the desk tells an
agent session that is already running what to do next ("run the e2e suite
and tell me", "stop, the release is off"), from any device, and follows the
work in one thread.

The project's rules shape the answer. The agent asks for the message, so
nothing reaches an agent that did not choose to listen. Only allow-listed
users are heard. hikyaku does not start agents: tools such as OpenClaw that
let a chat message start work on a desktop have had their Slack integration
become the attack path (CVE-2026-24764, channel metadata reaching the
prompt), and starting new work from Slack is left to tools that run the
agent in a sandbox.

## Requirements

- R1 `hikyaku listen [--name N] [--timeout D] [--json]` blocks until an
  instruction for it arrives, prints it, and exits 0. On timeout it exits
  124 and prints nothing on stdout, as `ask` does.
- R2 An instruction is a top-level message in the DM with the bot, from an
  allowed user (spec 001, R6). Messages in channels are never instructions,
  even from an allowed user, and neither are thread replies, which keep
  their spec 001 meaning.
- R3 The instruction is printed fenced, with a nonce in both markers, the
  author's ID and time, and marked as an instruction from Slack, in text
  and in `--json`. It carries a handle, so the agent can answer with
  `send --thread <handle>` or `ask --thread <handle>`, and replies in that
  thread follow spec 001's routing (D10).
- R4 Each listener has a name: `--name`, or by default the project name
  from spec 001's origin line, lower-cased. Names are letters, digits, `.`,
  `_` and `-`, up to 40 characters, and compared without case. A second
  listener with a name already waiting is refused. An instruction that
  starts with a waiting listener's name and a colon (`hayami: run the
  tests`) goes to that listener, with the name and colon removed. Any other
  instruction goes to the only listener when there is one; with several,
  it goes to none, and the bot answers in its thread with the names to use
  (D3). One instruction reaches at most one agent.
- R5 An instruction no listener takes is not kept: the bot answers in its
  thread that no agent is listening, and an agent that starts listening
  later never receives it (D4).
- R6 When a listener takes an instruction, the bot answers in its thread
  "picked up by" and the listener's origin line (spec 001, R12), so the
  person sees which agent has it (D5).
- R7 `hikyaku status` reports how many agents are listening and their names.
- R8 Nothing listens unless an agent runs `listen`. The service has no
  setting that makes it act on a DM by itself.

## Acceptance Criteria

- [x] AC1 Against the fake Slack: a top-level DM from an allowed user
  reaches a waiting `listen`, fenced with a nonce, labelled as an
  instruction from Slack, with its author, time and a handle, in text and
  `--json`; `send --thread` on that handle posts in the instruction's
  thread, and a reply there is routed by spec 001's rules. (R1, R3)
  `cli.TestAnInstructionReachesTheListener`.
- [x] AC2 A top-level DM from a user who is not allowed, a message in a
  channel, and a thread reply each reach no listener. (R2)
  `cli.TestOnlyAnAllowedDMIsAnInstruction`; the stranger is logged by ID
  and their text is not kept.
- [x] AC3 With two listeners named `alpha` and `beta`, `beta: x` reaches
  `beta` as `x`; an instruction with no name reaches neither and the bot's
  thread answer names both; with one listener, an instruction with no name
  (or with a word before a colon that is not a listener's name) reaches it.
  A second `listen --name alpha` is refused. (R4)
  `cli.TestInstructionsAreAddressedByName`, `cli.TestDefaultNameIsTheProject`.
- [x] AC4 With no listener, an instruction gets the "no agent is listening"
  answer in its thread, and a `listen` started afterwards does not receive
  it. (R5) `cli.TestAnInstructionNobodyTakesIsDropped`.
- [x] AC5 A taken instruction gets the "picked up by" answer with the
  listener's origin line. (R6) `cli.TestAnInstructionReachesTheListener`.
- [x] AC6 `listen` times out with exit 124 and empty stdout; `status`
  reports the listeners' names while they wait. (R1, R7)
  `cli.TestAnInstructionNobodyTakesIsDropped`; `status` in every listen test.
- [x] AC7 **Real Slack**: an instruction typed on a phone reaches a
  listening agent, the pickup answer appears, and the agent answers in the
  instruction's thread. (R1, R3, R6) E2E step 1, 2026-11-20.

## Decisions

- **D1 A listener holds one socket connection**, like `ask`'s wait: the
  service knows it is listening for exactly as long as the connection is
  open, and forgets it when the agent goes away.
- **D2 An agent that wants more instructions runs `listen` again.** One
  call takes one instruction, so the agent decides between instructions
  whether to keep listening.
- **D3 Instructions are addressed by name** (Q1, decided 2026-11-20). A
  word before a colon counts as an address only when it is a waiting
  listener's name, so "note: the build is slow" still reaches the only
  listener. Considered the longest-waiting listener taking it; rejected
  because the person cannot tell which agent will act. Considered allowing
  one listener at a time; rejected because several agents is the case this
  is for.
- **D4 An instruction nobody takes is dropped, and the bot says so** (Q2).
  Considered keeping it for a few minutes for the next listener; rejected
  because an instruction acted on later than the person expects is worse
  than one not acted on.
- **D5 Pickup is a thread answer** (Q3), with the existing `chat:write`.
  The reactions spec may replace it with a reaction.

## Threat model delta

S3 (what comes back from Slack) grows. Until now Slack text reached an
agent only as the answer to that agent's own question; with this spec, an
allowed user can start an exchange. The defences stay the same in kind:
the allow-list decides who is heard, nothing listens unless an agent chose
to, instructions come only from the DM (where nobody else can write), and
the text arrives fenced and labelled. The residual risk is stated plainly:
a compromised allowed account can now direct a listening agent, not only
answer it. The agent-side skill (spec 004) is where an agent is told to
confirm anything destructive before acting on it.

## Risks & Assumptions

- `im:history` already delivers top-level DMs to the bot (seen in spec
  001's real-Slack run), so no new scope is assumed.
- Rollback: revert; `listen` is a new command and the service changes are
  additive.

## E2E Test Plan

- Environment: the app from spec 001, the service headless on Linux, one
  or two agent sessions running `hikyaku listen`.
- Steps: (1) one listener, an instruction from a phone; the pickup answer
  appears and the agent answers in the thread (AC7, AC5). (2) two
  listeners, an instruction addressed to each and one addressed to
  neither; each reaches the right one and the third gets the list of names
  (AC3). (3) no listener, an instruction; the bot says no agent is
  listening (AC4).

### E2E Results

Run 2026-11-20 against the real workspace, service built from this branch
(`0.0.0-42858a5-dev`), headless on Linux.

| Step | Result |
|------|--------|
| 1 | Pass. A top-level DM reached `listen --timeout 30m` (listener `hikyaku`, the default name) fenced as an instruction with nonce, author ID, time and handle; exit 0. The service logged the pickup, and "Picked up." with the origin line appeared in the message's thread within a second. `send --thread` answered in the same thread. A further reply typed in that thread was filed under the answer's handle (spec 001 D10) and read with `inbox`. |
| 1a | Pass. An earlier `listen --timeout 15m` with no message sent exited 124 with nothing on stdout. |
| 2 | Not run against real Slack; covered by `cli.TestInstructionsAreAddressedByName` against the fake. |
| 3 | Not run against real Slack; covered by `cli.TestAnInstructionNobodyTakesIsDropped` against the fake. |

### Prod visibility

Not applicable: a desktop program with no production deployment.
