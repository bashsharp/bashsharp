---
name: bashy
description: Drive bashy, the agentic shell — a drop-in Bash 5.3 with agent-native extensions. Use whenever bashy is the shell you are running in (or is on PATH) to read what this host ALREADY KNOWS about your task before starting it, see what other agents and humans are saying, know what work is owed here, cut probing round-trips, preview destructive commands, run with structured result envelopes, navigate code without grep dances, and use environment-gated verified skills. Open every session with `bashy inspect context --json`, then `bashy kb search "<your task>"`, `bashy inbox`, and `bashy todo list`; close with `bashy kb retro`.
compatibility: requires the bashy binary (an agentic host shell); all verbs also work as `bashy <verb>` from any shell
---

# bashy — the agentic shell, in one page

bashy is two things at once: a **conformant Bash 5.3 drop-in** (your
scripts just run) and an **agentic shell** — a pure-Go userland and a set
of agent-native verbs that run *in-process*, identically on Linux, macOS,
and Windows. The extensions are additive: they never change what valid
bash means.

## First hop (do this once per session)

    bashy inspect context --json

One call replaces the usual probe dance (`uname`/`hostname`/`id`/`env`/
`which ...`): system + identity, resolved tool paths, safe environment
(secrets redacted by name), agent-mode flags, **skills applicable on this
host**, and a recommended-commands list. Use the reported `bashy_path`
for later calls.

## Second hop: durable state (kb · inbox · todo)

Everything else on this page helps you *do* the work. These three are the
state the work happens in — **what is known, what needs attention, and what
must be done** — and they are the only things that outlive your session.
Read them before starting; write to them before you finish. An agent that
skips them starts from nothing every time and leaves nothing behind.

**`kb` — what is KNOWN.** Distilled pages that agents before you wrote for
whoever came next: the gotcha that cost someone a stranded machine, the
build that only fails on Windows, the flag that silently does nothing. The
one verb that can tell you the task is already solved — or already known
to be a trap.

    bashy kb context --for "<task>" --rings repo,host --forms note,page --budget 700 --json
    bashy kb search "<the task, in your own words>"   # retrieve from one ring
    bashy kb show <slug>                              # read one record in full
    bashy kb observe --ring agent --episode E --kind tool-result --ref REF
    bashy kb validate <slug> --ring RING --from-gate EVENT_ID
    bashy kb note add --candidate --ring agent --episode E --title "…" --body "…"
    bashy kb retro                                    # reconcile after the work

These are the five harness stages: `context` assembles, `search`/`show`
retrieve, `observe` journals an event, `validate --from-gate` verifies, and
`note add --candidate` persists. A write selects exactly one store with
`--ring repo|host|agent`; context reads a named set with `--rings` and filters
record shapes with `--forms note,page,relation,code` (`kb search` uses singular
`--form`). `kb backlinks` resolves incoming links, `kb doctor` only flags
problems and never fixes them, and `kb transfer --from memex` is the supported
legacy import path.

Agents must never edit a kb store's `pages/*.md` or `graph.jsonl` by hand.
Use the `bashy kb` verbs: they enforce attribution, candidate status, duplicate
checks, redaction, ring ownership, relation ids, and append-only history.

A miss is honest and cheap: search reports which of your words the corpus
does not carry and which it does, so you can tell *"nobody knows this"*
from *"I asked in the wrong words"*, and reformulate instead of guessing.
If nothing relevant exists and the task taught something durable,
contribute it — `bashy kb add --type gotcha --title "…" --description
"what + WHEN this applies"` — distilled strategy, not a transcript, with
failures phrased as guardrails.

**`inbox` — what needs your ATTENTION.** One read-through view over MB,
standing Meet boards, Bus notifications, and authorized role mail. MB remains
the public send/history surface; inbox prevents transport-by-transport polling.

    bashy inbox                  # read every inbound source
    bashy skill show inbox
    bashy mb post "<message>"    # to everyone
    bashy mb send <agent> "…"    # to one agent, or a selector

For status that must remain discoverable outside terminal history, use the
explicit principal mailbox. Lists and searches do not consume; only `ack` takes
an item out of the pending view.

    bashy inbox list --topic harness --search timeout
    bashy inbox human list --topic posix-cert --project dhnt
    bashy inbox human send --topic posix-cert --project dhnt --status blocked \
      --ref docs/status.md "Profile D needs review"

Agent and human mailboxes share the same filters and JSON schema. `read` keeps
an item pending, `ack` is explicit, `preserve` reopens it, and `--all` includes
acknowledged history. Keep status under 1024 UTF-8 bytes and link details by a
stable shared path, commit, issue, room, or artifact reference.

Never consume board messages silently. After a read returns posts, show the
full message in the user-visible session console when short; otherwise show the
sender/topic, a concise summary, and the action it requires. Tool output may be
collapsed, so internal receipt alone is not operator-visible coordination.

**`todo` — what must be DONE.** The work list, scoped automatically the
way kb is: this repo's when you are in one, the host's otherwise.

    bashy todo list              # what is open here, priority first
    bashy todo add "<title>"     # record work so it outlives this session
    bashy todo start N / done N  # move it

## Run commands like an agent, not like a human

- Preview before you mutate: `bashy --dry-run SCRIPT` (agent-readable
  manifest with `BASHY_AGENTIC=1`).
- Preflight a script: `bashy check --agent --script SCRIPT`.
- Run with a captured, structured result envelope:
  `bashy run --check --capture -- SCRIPT`.
- One-command capability lookup (flags, features — skip trial and error):
  `bashy commands COMMAND --features`.
- On failures, read stderr hints: the space-time advisor explains
  environment-determined failures (wrong cwd, missing tool, full disk)
  so you do not retry a doomed command.

Before you run something you did not write, ask what running it amounts to.
`bashy inspect actions [--json] [--kind command|script|agent|skill]` lists every
action this bashy can run as one facet row — `kind name identity contract
latitude authority effects_declared executor` — across four families: a
**command** is exact and deterministic (the shell runs what was typed; effects
come off its atlas record), an **agent** binding is judge/agentic by definition
(executor `agentlaunch:<tool>`, so its cost and effects are open), a **skill**
is bound by the strongest contract it carries (`dhnt` face with declared
effects, `metadata-checks`, or `none`; one judge step makes the whole run
agentic), and **script** is a family nothing fills yet (reported as `0`, never
omitted). `bashy define NAME` prints the same facet for one name (`runs:` line;
nested `action` object under `--json`), and `bashy inspect context --json`
carries the counts as `actions: {command, script, agent, skill}`. Prefer an
exact/deterministic row when one satisfies the task; reach for a judge/agentic
one only when the task needs the latitude.

## Navigate code without the grep dance

- `bashy graph impact SYMBOL` — what code is coupled to a symbol.
- `bashy ast symbols PATH` / `bashy ast search PATTERN` /
  `bashy ast refs SYMBOL` / `bashy ast map` — treesitter-backed,
  model-free.
- Shared repo memory (an agentic wiki other agents' findings accrue
  into): `bashy graph recall QUERY` to read; `bashy graph note` /
  `bashy graph observe` to contribute; `bashy graph pitfalls` before
  risky changes.

## Skills: verified procedures, gated to this host

- `bashy skill list` — only skills applicable at THIS host's coordinate
  (env-gated); `bashy skill show NAME` to read one.
- `bashy skill run NAME` — execute a machine-checkable skill; the
  success contract is verified and every run leaves a re-checkable
  attestation. Exit 0 iff the contract held.
- `bashy skill run NAME --adapt --repair-agent "<your headless CLI>"`
  — self-heal a failing skill; verified fixes are learned once per host
  and reused by every agent.
- Contribute back: author a skill folder, then `bashy skill learn DIR`
  (admission requires the contract to actually hold here) and
  `bashy skill promote NAME` (human-reviewed bundle — never
  auto-published).

## Edit the catalog through verbs, never by hand

Never edit files under `~/.config/bashy/` (or any `BASHY_*_DIR`) directly:
embedded entries are immutable and every write is copy-on-write into the
local ring, which only the verbs do. `bashy <noun> schema` lists the dotted
paths a `tool`/`model`/`agent` accepts; `bashy <noun> set NAME --set
path=value` (`--unset path`) edits one, `bashy <noun> show NAME --field
path` reads it back. Skills: `bashy skill add NAME --description "…"`
mints one, `bashy skill set NAME` / `bashy skill rm NAME` edit and remove,
`bashy skill show NAME --yaml` prints its record (`skill add FILE.yaml|-`
re-imports it).

## Fleet and workspace (when the task outgrows one session)

- `bashy weave …` — isolated per-issue workspaces for parallel agent
  runs; `bashy sprint …` — plan/continuity; `bashy dag TASKS.md` —
  markdown-defined task DAGs. Read the `conductor` skill
  (`bashy skill show conductor`) before orchestrating a fleet.

## Rules of thumb

1. `bashy inspect context --json` first; trust it over your own probes.
2. **Open with `kb search` + `inbox` + `todo list`; close with `kb retro`.**
   If you remember three verbs from this page, remember those. Everything
   else here helps you do the task; only these tell you whether it is
   already solved, whether someone is talking to you about it, and what
   else is owed here. They are also the only ones whose value compounds —
   what you write back is what the next agent finds instead of
   rediscovering, and the next agent is usually you.
3. Prefer `bashy run`/`--dry-run` envelopes over raw execution when the
   command mutates state.
4. Before re-deriving a procedure, check `bashy skill list` — a
   verified, attested skill may already exist; after solving something
   reusable, consider contributing it back with `skill learn`.
5. The userland (ls/grep/sed/…) is in-process and identical on every
   platform — Windows included; do not shell out to platform-specific
   alternatives.
