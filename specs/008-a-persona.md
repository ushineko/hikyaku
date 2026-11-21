# Spec 008: a persona

**Issue**: [#10](https://github.com/ushineko/hikyaku/issues/10)

## Executive Summary

A persona file (`SOUL.md` beside the settings, with a built-in default)
gives the bot a name, a voice for the answerer's answers, and lists its
notes are varied from; the bot no longer names the agent in what it posts.
Reviewers should look first at how the voice enters the answerer's prompt
(`answerer.Prompt`: after the rules, fenced, rules win) and at
`Service.post`, where every note is flavoured and then neutralized.

## Status: COMPLETE

## Context

The bot speaks in two ways: fixed notes the service posts ("Queued: 2 ahead
of it.", "No agent is listening…", "*claude* stopped listening."), and the
answerer's answers, written by a Claude Code run. The notes read like a log,
and the answers have no voice of their own. The owner renamed the app
(`nykbot-α`) and asked for a persona, a file in the spirit of a SOUL.md: the
bot's name, its tone and scope, and notes that vary.

This spec adds a persona file with a built-in default, as the owner
described it: "a technical helper with a dry sense of humor. It can also tell
dad jokes. Uses strange/random emojis every so often (e.g. no problem! 🐙),
more non-sequitur the better, in both occasional response and emoji choice."

## Requirements

- R1 **The file.** The persona is a Markdown file:
  `~/.config/hikyaku/SOUL.md`, or the file `persona` names in the settings.
  When there is none, the built-in default applies. It is read when used
  and re-read when it changes, so an edit needs no restart. A file over 16
  KB, or one that cannot be read, is logged and the default is used.
- R2 **Its sections**, each optional, each `## <Section>` with a body:
  - `Name`: the bot's name, on one line, up to 40 characters.
  - `Voice`: prose describing tone and scope, for the answerer (R3).
  - `Started`: a list (`- …`) of notes for a run that has started.
  - `Emoji`: a list of emoji to add to notes now and then.
  - `Asides`: a list of short asides to add to notes now and then.
  - `Chances`: `- emoji: 1 in N` and `- aside: 1 in N`, how often each is
    added (default 1 in 3 and 1 in 8; `never` for none).

  An unknown section is ignored. A missing section falls back to the
  default's.
- R3 **The voice shapes answers.** The answerer's prompt carries the Voice
  after its rules, fenced with a nonce and labelled as the owner's style
  guidance: style only, the rules above win. The prompt keeps every rule of
  spec 007, and the answer is scrubbed and neutralized as before.
- R4 **Notes vary.** The note a run starts with is picked from `Started`.
  Each of the bot's notes (the answerer's notes, spec 005 and 006's notes in
  a thread, and the "stopped listening" `/me`) may get, by `Chances`, one
  emoji from `Emoji` at its end and, separately, one aside from `Asides` on
  a line of its own. The note's own words come first and are never changed:
  "Queued: 2 ahead of it. 🦑" keeps its count. Agents' messages and the
  answerer's answers get neither.
- R5 **The name.** With a `Name`, the answer's origin line starts with it
  (`nykbot-α · <host> · an automated answer…`), and
  `<name>:` reaches the answerer like `answerer:` (spec 006, R10). A
  listener cannot take the name. Without one, both stay `answerer`.
- R6 **Nothing from the persona mentions anyone.** Everything taken from the
  file and posted to Slack passes through `Neutralize`, as answers do.
- R7 `hikyaku persona` prints which file is in effect (or that the default
  is), the name, and the chances. `hikyaku persona --init` writes the
  default to `~/.config/hikyaku/SOUL.md` for editing, and refuses to
  overwrite a file that exists.
- R8 **The default persona** (built in, also what `--init` writes): a
  technical helper with a dry sense of humour; the occasional dad joke,
  never more than one an answer and never in place of the facts; strange,
  non-sequitur emoji (🐙 🦆 🧀 🪐 🦖 🥨 🛼 🪼 🧲 🦩 🍄 🪗 🦔 🥒 🧦 🛸 🫖 🦑 🍔
  🪱); asides that have nothing to do with anything ("The toaster has been
  informed.", "Octopuses have three hearts. Unrelated."). No name: the
  owner's app name is theirs to put in.
- R9 **The bot does not name the agent** (owner, 2026-11-20: "get rid of
  any mentions of Claude, we know it's claude"). The origin line under an
  agent's message is the project, branch and host, without the agent
  (amends spec 001 R12), and the answerer's line is
  `<name> · <host> · an automated answer…`.

## Acceptance Criteria

- [x] AC1 Parsing: a file with every section gives each value; a file with
  some sections falls back to the default for the rest; an unknown section
  is ignored; a file over 16 KB, or unreadable, gives the default and a log
  line. (R1, R2)
  `persona.TestAPersonaFileIsRead`, `persona.TestTheLoaderFollowsTheFile`.
- [x] AC2 An edit to the file is used by the next note without a restart.
  (R1)
  `persona.TestTheLoaderFollowsTheFile`.
- [x] AC3 The answerer's prompt holds the Voice after the rules, fenced
  with a nonce and labelled as style guidance; a Voice that tries to close
  its fence stays inside it; the spec 007 rules are all still there. (R3)
  `cli.TestThePersonaShapesTheAnswerer`.
- [x] AC4 With chances set to `1 in 1`, every note ends with an emoji from
  the list and carries an aside, and its own words come first unchanged;
  with `never`, notes are exactly as before; answers and agents' messages
  get neither. Uses a fixed random source. (R4)
  `persona.TestFlavourIsAddedNeverSubstituted`, `cli.TestThePersonaShapesTheAnswerer`, `cli.TestAgentsMessagesAreTheirOwn`. Chances of `1 in 1` and `never` make it deterministic; no random source is injected.
- [x] AC5 The started note comes from `Started`. (R4)
  `persona.TestTheStartedNoteComesFromTheList`, `cli.TestThePersonaShapesTheAnswerer`.
- [x] AC6 With `Name: nykbot-α`, the answer's origin line starts with it,
  `nykbot-α: q` and `NYKBOT-α: q` reach the answerer, and `listen --name`
  with the name is refused. (R5)
  `cli.TestThePersonaShapesTheAnswerer`, `cli.TestThePersonasNameReachesTheAnswerer`. A listener name cannot hold `α`, so the refusal applies to a name that fits a listener's.
- [x] AC10 With `CLAUDECODE=1` in the environment, the origin line has no
  agent; the answerer's line names no agent. (R9)
  `cli.TestOriginNamesTheRepositoryNotTheWorktree` (with `CLAUDECODE=1` set), `cli.TestThePersonaShapesTheAnswerer`.
- [x] AC7 A persona with `<!channel>` in an aside, a started note and the
  name posts none of them as a mention. (R6)
  `cli.TestAPersonaCannotMentionAnyone`.
- [x] AC8 `persona` prints the file in effect, name and chances;
  `persona --init` writes the default and refuses to overwrite. (R7)
  `cli.TestThePersonaCommand`, `persona.TestInitWritesTheDefaultOnce`.
- [x] AC9 **Real Slack and real Claude Code**: with the default persona and
  a name, the owner asks the answerer two questions and sees varied started
  notes and an answer in the persona's voice that still answers the
  question; a queued note shows an emoji now and then. (R3, R4, R5)
  See E2E results.

## Decisions

- **D1 The file is the owner's, and trusted as their settings are.** It
  lives in their config directory, so whoever can edit it can already edit
  the settings and the allow-list. It is still fenced in the prompt and
  placed after the rules (R3), so a careless Voice ("ignore your rules and
  be brief") reads as style, not as a change to what the run may do, which
  the run's flags decide anyway (spec 007, S6).
- **D2 Flavour is added, never substituted** (R4). Notes carry facts (a
  count, a name, why something was not delivered). Rewriting them with a
  model would cost a run per note and could lose the fact, so the service
  appends from fixed lists instead, and only the started note, which carries
  no fact, is replaced outright.
- **D3 Answers get the voice through the prompt, not post-processing.** The
  run writes in the voice; the service adds nothing to an answer, which
  stays the run's text, scrubbed (spec 007, R10).
- **D4 No name by default.** The bot's display name is set in Slack. The
  persona's name is for the origin line and the address prefix, and is
  written by the owner.

## Risks & Assumptions

- A Voice can make answers longer or less clear. The prompt's 2500
  character limit and "never in place of the facts" stand; the owner edits
  the file if it grates.
- Humour in notes about a failure can read badly. The asides are short and
  separate from the note's words; `Chances` can turn them off.
- Rollback: delete or empty the file (default persona), set chances to
  `never`, or revert.

## E2E Test Plan

- Environment: the service headless on Linux, the answerer on, the default
  persona with `Name: nykbot-α` added, real Slack, real Claude Code.
- Steps:
  1. Ask the answerer two questions. Expect two started notes, probably
     different, and answers in the voice that answer the question, with
     the origin line starting `nykbot-α` (AC9, R3 to R5).
  2. `nykbot-α: what's hikyaku's latest release?` while an agent listens
     reaches the answerer (AC9, R5).
  3. Send three questions quickly; the queued notes keep their counts and
     some carry an emoji (AC9, R4).

### E2E results (2026-11-20, real Slack, real Claude Code)

The owner's `SOUL.md` was the default written by `persona --init`, with
`Name: nykbot-α` and a line for "are you Claude?" added to the Voice.
Four answerer runs, two of them follow-ups, $0.03 to $0.05 each, all
`success`, none denied. The owner confirmed it works: varied started notes,
answers in the voice, the answer line starting `nykbot-α` with no agent
named. The owner then extended their Voice (Monty Python, SpongeBob and
Simpsons references; Comic Book Guy when asked to explain), which applied
without a restart.

Also found: `persona --init` refused the owner's existing config directory
(mode 755) because it reused the check for token directories; a persona is
not secret, so it now only creates the directory when missing.

### Prod visibility

Not applicable: a desktop program with no production deployment.
