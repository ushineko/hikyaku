# Spec 006: a listener that stays

**Issue**: [#6](https://github.com/ushineko/hikyaku/issues/6)

## Executive Summary

A listening agent's name is now held after each `listen` (30 minutes by
default), so messages for a busy agent wait for it instead of being
dropped; the agent answers beside the person's message with `--reply-to`,
and a conversation can run without threads. Reactions (⏳ 👀 ✅ 📭) replace
the thread notes, a `/me` notice says when an agent stopped listening, and
`answerer:` reaches the answerer while an agent holds the DM. Reviewers
should look first at `internal/service/listen.go` (the hold, queue and
routing under one lock) and the threat model's S3 delta.

## Status: COMPLETE

## Context

Spec 005 lets a running agent take an instruction typed in the DM with the
bot. One `listen` call takes one instruction and ends. While the agent works
on it, nothing is listening: a new top-level message gets "No agent is
listening" (or, since spec 007, goes to the answerer). The real-Slack test of
spec 005 ran into this as soon as the agent was in a conversation.

Every answer also goes into a thread today, so a conversation with one agent
reads as a list of threads rather than a chat.

This spec keeps a listener reachable while its agent is busy, lets the agent
answer where the person wrote, shows what happened to a message with
reactions instead of notes in its thread, and says in Slack when a listener
has stopped. It covers the DM only. Instructions from a channel the bot
joins are a later spec.

**Rescoped** (owner, 2026-11-20) after spec 007 shipped: the answerer now
covers "a DM that is always listening", so this spec is about a running
agent session. The planned reactions spec is folded in (R2, R3, R5), and
`/me` status notes are dropped in favour of reactions, except the "stopped
listening" notice. The routing between agents and the answerer is settled
(R10).

## Requirements

- R1 **Hold.** When a `listen` call ends, by taking an instruction, by its
  timeout, or by its client going away, the service holds the listener's
  name for `--hold` (default 30 minutes; 0 for spec 005's behaviour; at
  most 24 hours). While held, the listener counts as listening for routing
  (spec 005 R4) and for `status`, and a `listen` with that name resumes it.
  A `listen` with the name of a listener that is waiting (a `listen` call is
  open) is still refused. `answerer` is not a listener name (R10).
- R2 **Queue.** An instruction routed to a held listener is queued for it
  and gets the ⏳ reaction. The listener's next `listen` receives the
  oldest queued instruction at once. At most 10 are queued per listener;
  one more is not delivered and is answered in its thread "Not delivered:
  *name* already has ten instructions waiting."
- R3 **Gone.** A hold that ends with no `listen` resuming it removes the
  listener. Each instruction still queued for it loses ⏳, gets 📭, and is
  answered in its thread "Not delivered: *name* stopped listening." The
  bot posts a `/me` message in the DM: "stopped listening for *name*.",
  which Slack shows after the bot's own name. A
  listener resumed within its hold posts nothing. With `--hold 0`, a
  listener that took an instruction ends silently, as in spec 005; one that
  timed out or whose client went away posts the notice.
- R4 **Answer where the person wrote.** `send --reply-to <handle>` and
  `ask --reply-to <handle>` post top-level in the DM when the handle is an
  instruction, or a message posted top-level with `--reply-to`; for any
  other of hikyaku's messages, they post in its thread (as `--thread`).
  Like `--thread`, they carry no origin line and no reply hint, and cannot
  be combined with `--channel` or `--thread`. `listen`'s hint on stderr
  names `--reply-to`. `--thread` keeps its meaning.
- R5 **Reactions show what happened to a message**: ⏳ queued (R2), 👀 taken
  by a listener (replacing spec 005's "Picked up." note), ✅ answered (the
  first `send` or `ask` with `--reply-to` or `--thread` on the
  instruction's handle; 👀 is removed), 📭 not delivered (R3). Notes in the
  thread remain only where a reaction cannot say enough: "No agent is
  listening", "Several agents are listening", and the two "Not delivered"
  answers. Without the `reactions:write` scope (an app not yet
  reinstalled), each reaction falls back to its old note or a short one in
  the thread, and the service logs the missing scope once.
- R6 **A conversation without threads.** A message posted with
  `--reply-to` on an instruction belongs to that instruction's listener.
  While an `ask` waits on such a message, the next top-level message routed
  to that listener is its reply, before any waiting or held `listen` gets
  it. A reply in the message's thread also answers it, as in spec 001.
- R7 `status` shows each listener as waiting, busy (held, with how many
  instructions are queued), or in a conversation (an `ask` from R6 waits).
- R8 Queues and holds live only in the service's memory. A restart drops
  them without a "Not delivered" answer; the README says so.
- R9 **A `/me` from a person is a message**: as an instruction when it is
  top-level in the DM, as a reply when it is in one of hikyaku's threads.
  Today it is dropped with the other message subtypes. A `/me` from a bot,
  the service's own "stopped listening" included, is not.
- R10 **Agents first; `answerer:` reaches the answerer.** A top-level DM
  goes to a waiting or held listener before the answerer (spec 007 D2). A
  message from the owner that starts with `answerer:` goes to the answerer
  whatever is listening, without the prefix; with the answerer off it is
  answered "The answerer is off." in its thread.

## Acceptance Criteria

- [x] AC1 Against the fake Slack: a listener takes an instruction (👀); a
  second instruction sent before its next `listen` gets ⏳ and reaches that
  next `listen`, which swaps ⏳ for 👀. (R1, R2, R5)
  `TestAHeldListenerQueuesAndTakes`.
- [x] AC2 A held listener counts for routing: with `alpha` held and `beta`
  waiting, an instruction with no name reaches neither and the answer
  names both; `alpha: x` is queued for `alpha`. A second `listen --name
  alpha` while `alpha` waits is refused; one while `alpha` is held resumes
  it. `listen --name answerer` is refused. (R1, R10)
  `TestAHeldListenerCountsForRouting`.
- [x] AC3 The eleventh queued instruction is answered as not delivered and
  is never received. (R2)
  `TestTheQueueHoldsTen`.
- [x] AC4 A hold that ends with instructions queued gives each 📭 and its
  note, and posts one "stopped listening" `/me`; a `listen` that times out
  with `--hold 0` posts it at once; a listener resumed in time posts
  nothing; one that took an instruction with `--hold 0` posts nothing. Uses
  short holds. (R1, R3)
  `TestAHoldThatEndsSaysSo`.
- [x] AC5 `send --reply-to` on an instruction posts top-level in the DM,
  with no origin line, and puts ✅ on the instruction in place of 👀; on a
  message posted that way, top-level again; on another of hikyaku's
  messages, in its thread. `--reply-to` with `--thread` or `--channel` is a
  usage error. (R4, R5)
  `TestReplyToAnswersWhereThePersonWrote`.
- [x] AC6 `ask --reply-to` on an instruction receives the person's next
  top-level message as its reply, while a `listen` with the same name is
  held, and that message is not queued for the listener; a reply typed in
  the question's thread also answers it. (R6)
  `TestAConversationWithoutThreads`.
- [x] AC7 `status` shows waiting, busy with a count, and in a
  conversation. (R7)
  `TestAHeldListenerQueuesAndTakes`, `TestAConversationWithoutThreads`.
- [x] AC8 Without `reactions:write` (the fake answers `missing_scope`),
  taking an instruction posts "Picked up." in its thread and queuing posts a
  "Queued" note, and the service logs the missing scope once. (R5)
  `TestWithoutTheReactionsScopeNotesStandIn`.
- [x] AC9 A `/me` from an allowed user is taken as an instruction
  (top-level) and as a reply (in a thread); one from a user who is not
  allowed, and one from a bot, reach no agent. (R9)
  `TestAMeIsAMessage` (the bot's `/me` is sent under an allowed user's ID, so the bot filter, not the allow-list, is what stops it).
- [x] AC10 With a listener waiting and the answerer on, an owner's
  `answerer: q` starts a run with `q` and the listener gets nothing; with
  the answerer off, it is answered "The answerer is off.". (R10)
  `TestAnswererPrefixBypassesListeners`.
- [x] AC11 **Real Slack**, after the app is reinstalled with
  `reactions:write`: one agent keeps a top-level conversation over three
  messages with `ask --reply-to` and `listen`; a message sent while the
  agent is busy gets ⏳, then 👀, then ✅; with `--hold 1m`, letting it run
  out posts the "stopped listening" `/me`; a `/me` typed to the bot is taken
  as an instruction; `answerer: …` reaches the answerer while the agent is
  held. (R1 to R6, R9, R10)
  See E2E results.

## Decisions

- **D1 Answer where the person wrote** (2026-11-20). A top-level message
  gets a top-level answer, a thread reply a thread answer; there is no
  marker to choose. Considered always answering in a thread (spec 005);
  rejected because a conversation with one agent became a list of threads.
- **D2 Hold 30 minutes by default** (2026-11-20), so an agent that runs a
  test suite between instructions stays reachable. The ⏳ reaction shows at
  once that a message waits, which answers spec 005 D4's concern (an
  instruction acted on later than expected), and the instruction carries
  the time it was typed.
- **D3 Reactions, not notes** (owner, 2026-11-20). Reactions keep the DM
  readable as the conversation and need one scope, `reactions:write`.
  Considered `/me` notes naming the agent (no new scope, but each is a
  message that notifies, and Slack's thread support for `chat.meMessage` is
  undocumented) and plain notes (today's). A reaction cannot name which
  agent took a message; with several agents, the person addressed one by
  name already.
- **D4 "Stopped listening" is a top-level `/me`** (2026-11-20). It is the
  one notice about the agent rather than a message, so it goes in the DM's
  flow as the bot's narration; `chat.meMessage` posts top-level, which is
  documented. It is driven by the hold timer this spec adds.
- **D5 Queues are not persisted** (R8). A restarted service has no
  listeners, so a kept queue would have no one to deliver to.
- **D6 The same name resumes a held listener.** The service cannot tell
  two agents apart (both are the same user on the same socket), so a
  second agent using a held name takes it over. Names default to the
  project, so two agents in one project should pass `--name`.
- **D7 Agents first, `answerer:` to bypass** (owner, 2026-11-20). An agent
  the owner started is doing something for them, so an unaddressed message
  is most likely for it. Considered sending unaddressed messages to the
  answerer while an agent is held; rejected because a reply to the agent
  would then start a paid run. The persona's name (a later spec) may become
  a second prefix.
- **D8 A timed-out `listen` is held too** (2026-11-20). An agent that loops
  on `listen` with a short timeout should not post "stopped listening"
  between calls, so a timeout starts a hold like any other end.

## Threat model delta

S3 changes in two ways. An instruction can now be acted on up to the hold
time after it was typed, not only at once; the ⏳ reaction says so when it
is queued, and the instruction carries its time. And the bot can add
reactions (`reactions:write`), which it uses only on messages in the DM it
was given. Nothing else changes: the allow-list, DM-only instructions and
the fence are as in spec 005, a `/me` passes the same allow-list as any
message, and the queue holds only allowed users' text, in memory.

## Risks & Assumptions

- `reactions:write` needs the app reinstalled; until then R5 falls back to
  notes, so nothing breaks.
- `chat.meMessage` needs only `chat:write`. If it fails, the notice is
  posted as a plain message.
- Rollback: revert. `--hold 0` gives spec 005's behaviour, apart from
  reactions, without a revert.
- A listener that never resumes posts "stopped listening" up to the hold
  time after its last `listen`, not at once: the service cannot tell a busy
  agent from a gone one sooner.

## E2E Test Plan

- Environment: the app from spec 001 reinstalled with `reactions:write`,
  the service headless on Linux, one agent session, the answerer on.
- Steps:
  1. The agent listens; the person sends a top-level message (👀); the
     agent answers with `ask --reply-to` (✅); the person answers top-level;
     twice more (AC11, R4 to R6).
  2. While the agent is answering, the person sends another message: ⏳,
     then 👀 when the next `listen` takes it (AC11, R2, R5).
  3. `answerer: what's the latest release of hikyaku?` while the agent is
     held reaches the answerer (AC11, R10).
  4. The person sends `/me waves` top-level; the agent's `listen` gets it
     (AC11, R9).
  5. The agent listens with `--hold 1m` and stops; a minute later the
     "stopped listening" `/me` appears (AC11, R3).

### E2E results (2026-11-20, real Slack, app reinstalled with `reactions:write`)

The agent was this Claude Code session, listening as `claude` with
`--hold 10m`, then `--hold 1m`; the service ran headless from the branch.

| Step | Result |
| --- | --- |
| 1 Top-level conversation | Pass. The instruction got 👀 then ✅; three answers were posted top-level with `ask --reply-to`/`send --reply-to`, and each of the person's top-level answers reached the waiting `ask` ("a message answered a conversation's question" in the log), not the listener |
| 2 Message while busy | Pass. It got ⏳ at once (`claude (busy, 1 queued)`); the next `listen` returned it immediately and it turned to 👀, then ✅ on the answer. The ⏳ was confirmed with Slack (`already_reacted`) when the client was slow to show it |
| 3 `answerer:` while held | Pass. The answerer ran ($0.20) and answered in the thread; a reply there ran as a follow-up; a message sent meanwhile without the prefix was queued for `claude` |
| 4 `/me waves` | Pass. Arrived as `subtype=me_message` and reached `listen` as the instruction "waves" |
| 5 Hold runs out | Pass. "a listener's hold ended" a minute after the last `listen`, and the `/me` notice appeared. The owner found "*claude* stopped listening" read oddly after the bot's name, as a `/me` does; it now reads "stopped listening for *claude*" |

Also found: the bot's display name in messages stayed `hikyaku` after the
app was renamed and reinstalled, although the app profile shows the new
name. That is Slack's bot user lagging the app settings, not hikyaku,
which sets no sender name.

### Prod visibility

Not applicable: a desktop program with no production deployment.
