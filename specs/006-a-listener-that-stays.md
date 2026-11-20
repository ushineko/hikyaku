# Spec 006: a listener that stays

**Issue**: [#6](https://github.com/ushineko/hikyaku/issues/6)

## Status: INCOMPLETE

## Context

Spec 005 lets a running agent take an instruction typed in the DM with the
bot. One `listen` call takes one instruction and ends. While the agent works
on it, or talks in its thread, nothing is listening: a new top-level message
gets "No agent is listening" and is dropped. The real-Slack test of spec 005
ran into this as soon as the agent was in a conversation.

Every answer also goes into a thread today, so a conversation with one agent
reads as a list of threads rather than a chat.

This spec keeps a listener reachable while its agent is busy, lets the agent
answer where the person wrote, and says in Slack when a listener has
stopped. It covers the DM only. Instructions from a channel the bot joins
are a later spec.

## Requirements

- R1 After a listener takes an instruction, the service holds its name for
  `--hold` (default 30 minutes, 0 for spec 005's behaviour, at most 24
  hours). While held, the listener counts as listening for routing (spec
  005 R4) and for `status`, and a `listen` with that name resumes it.
  A `listen` with the name of a listener that is waiting, not held, is
  still refused.
- R2 An instruction routed to a held listener is queued for it, and the
  bot answers in the instruction's thread "Queued: *name* is busy with an
  earlier instruction". The listener's next `listen` receives the oldest
  queued instruction at once, and the bot answers "Picked up." in that
  instruction's thread, as in spec 005. At most 10 instructions are
  queued per listener; one more is not delivered, and the bot says so.
- R3 When a hold ends with no `listen` resuming it, the listener is gone:
  every instruction still queued for it is answered "Not delivered: *name*
  stopped listening" in its thread, and the bot posts "*name* stopped
  listening" in the DM once. A listener whose `listen` times out or whose
  client goes away without having taken an instruction posts the same
  notice. A listener resumed within its hold posts nothing.
- R4 `send --reply-to <handle>` and `ask --reply-to <handle>` answer where
  the person wrote: for an instruction's handle, a top-level message in
  the same DM; for one of hikyaku's own messages, a message in its thread
  (as `--thread`). Like `--thread`, they carry no origin line and no reply
  hint, and cannot be combined with `--channel` or `--thread`. `listen`
  names `--reply-to` in its hint on stderr. `--thread` keeps its meaning.
- R5 A message posted with `--reply-to` on an instruction belongs to that
  instruction's listener. An `ask` waiting on such a message receives the
  next top-level message routed to that listener, as its reply, before any
  waiting or held `listen` does. A reply in the message's thread also
  answers it, as in spec 001. So a person can keep a conversation with one
  agent going in the DM without threads.
- R6 `status` shows each listener as waiting, busy (held, with how many
  instructions are queued), or in a conversation (an `ask` from R5 is
  waiting).
- R7 Queued instructions live only in the service's memory. A restart
  drops them without a "not delivered" answer; this is stated in the
  README.
- R8 A `/me` message from an allowed user counts as an ordinary message:
  as an instruction when it is top-level in the DM, as a reply when it is
  in one of hikyaku's threads. Today it is dropped with the other message
  subtypes. The bot's own `/me` messages (D7) are never taken as a
  person's.
- R9 The bot's status notes ("Picked up.", "Queued", "Not delivered",
  "stopped listening", and spec 005's "No agent is listening" and
  list of names) are posted as `/me` messages, naming the agent where
  there is one ("Picked up by *hikyaku · main · Claude Code*"), where
  Slack allows it (D7).

## Acceptance Criteria

- [ ] AC1 Against the fake Slack: a listener takes an instruction; a second
  instruction sent before its next `listen` is answered "Queued" and
  reaches that next `listen`, whose thread then gets "Picked up.". (R1, R2)
- [ ] AC2 A held listener counts for routing: with `alpha` held and `beta`
  waiting, an instruction with no name reaches neither and the answer
  names both; `alpha: x` is queued for `alpha`. A second `listen --name
  alpha` while `alpha` waits is refused; one while `alpha` is held resumes
  it. (R1)
- [ ] AC3 The eleventh queued instruction is answered as not delivered and
  is never received. (R2)
- [ ] AC4 A hold that ends with instructions queued answers each "Not
  delivered" and posts one "stopped listening" message; a `listen` that
  times out with nothing taken posts it too; a listener resumed in time
  posts nothing. Uses an injected clock, not real waiting. (R3)
- [ ] AC5 `send --reply-to` on an instruction posts top-level in the DM,
  with no origin line; on one of hikyaku's messages, in its thread.
  `--reply-to` with `--thread` or `--channel` is a usage error. (R4)
- [ ] AC6 `ask --reply-to` on an instruction receives the person's next
  top-level message as its reply, while a `listen` with the same name is
  held; a reply typed in the question's thread also answers it. (R5)
- [ ] AC7 `status` shows waiting, busy with a count, and in a
  conversation. (R6)
- [ ] AC8 **Real Slack**: one agent keeps a top-level conversation in the
  DM over three messages using `send --reply-to` / `ask --reply-to` and
  `listen`; a message sent while the agent is busy is answered "Queued"
  and later picked up; letting the hold run out (with `--hold 1m`) posts
  "stopped listening". (R1 to R5) The status notes appear as `/me`
  messages (or as plain thread answers, if D7's check found `/me` cannot
  go in a thread), and a `/me` typed to the bot is taken as an
  instruction. (R8, R9)
- [ ] AC9 Against the fake Slack: a `/me` from an allowed user is taken as
  an instruction (top-level) and as a reply (in a thread); a `/me` from a
  user who is not allowed, and one from the bot, reach no agent. (R8)
- [ ] AC10 Against the fake Slack: the status notes are posted with the
  `/me` method and say which agent; the agent's own answers are not.
  (R9)

## Decisions

- **D1 Answer where the person wrote** (decided 2026-11-20). A top-level
  message gets a top-level answer, a thread reply a thread answer; there
  is no marker to choose. Considered always answering in a thread (spec
  005); rejected because a conversation with one agent became a list of
  threads. Considered a marker in the message; rejected as one more thing
  to remember.
- **D2 Hold 30 minutes by default** (decided 2026-11-20), so an agent that
  runs a test suite between instructions stays reachable. The person is
  told at once that a message is queued, which answers spec 005 D4's
  concern (an instruction acted on later than expected): the delay is
  visible, and the instruction carries the time it was typed.
- **D3 The bot's own status answers stay in threads.** "Queued", "Picked
  up." and "Not delivered" go in the instruction's thread; only the
  agent's answers and the "stopped listening" notice are top-level, so the
  DM reads as the conversation. The reactions spec may replace the thread
  answers with reactions.
- **D4 The "stopped listening" notice moves here** from the planned
  reactions spec: it is driven by the hold timer this spec adds.
- **D5 Queues are not persisted** (R7). A restarted service has no
  listeners, so a kept queue would have no one to deliver to.
- **D6 The same name resumes a held listener.** The service cannot tell
  two agents apart (both are the same user on the same socket), so a
  second agent using a held name takes it over. Names default to the
  project, so two agents in one project should pass `--name`.

- **D7 Status notes are `/me` messages** (decided 2026-11-20). The bot
  narrates ("_nickbot: Picked up by hikyaku · main_") rather than
  addressing the person, needs no scope beyond `chat:write`
  (`chat.meMessage`), and can name the agent, which a reaction cannot.
  Unknown: whether Slack accepts a `/me` inside a thread; the documented
  thread parameter is on `chat.postMessage`. This is checked against
  real Slack before implementing R9. If it cannot, notes in threads stay
  plain messages and only the top-level "stopped listening" is a `/me`.
  A `/me` still notifies like a message; reactions (their own spec)
  remain the quieter option.

## Threat model delta

S3 changes in one way: an instruction can now be acted on up to the hold
time after it was typed, not only at once. The bot says so when it queues
one, and the instruction carries its time. Nothing else changes: the
allow-list, DM-only instructions and the fence are as in spec 005, and the
queue holds only allowed users' text, in memory.

## Risks & Assumptions

- No new Slack scope: everything is posted with `chat:write`.
- Rollback: revert. `--hold 0` gives spec 005's behaviour without a revert.
- A listener that never resumes posts "stopped listening" up to 30 minutes
  after its last instruction, not at once: the service cannot tell a busy
  agent from a gone one sooner.

## E2E Test Plan

- Environment: the app from spec 001, the service headless on Linux, one
  agent session.
- Steps: (1) the agent listens; the person sends a top-level message; the
  agent answers with `ask --reply-to`; the person answers top-level; twice
  more (AC8, R4, R5). (2) while the agent is answering, the person sends
  another message; it is answered "Queued" and is picked up by the next
  `listen` (AC8, R2). (3) the agent listens with `--hold 1m` and stops; a
  minute later "stopped listening" appears (AC8, R3).

### Prod visibility

Not applicable: a desktop program with no production deployment.
