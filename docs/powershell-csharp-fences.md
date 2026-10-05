# PowerShell and C# fences — the language contract

Status: **design of record, settled 2026-10-02 (operator decision); no engine
code yet.** This page answers S358.0 (`30bb5abea933`) of
`docs/sprint-358-master-execution-plan.md`. It fixes the language contract for
two new Bash# source fences, `powershell` and `csharp`, before any evaluator,
lowerer or provisioner work. Every spelling, value rule and verdict below is a
decision a later story implements, not a description of shipped behaviour.

## Where these fences sit — the interface hierarchy

Bash# is its own language and bashy its own system; they are not an imitation
of an OS or of another shell. The goal of these two fences is to let a Windows
user bring familiar PowerShell and C# code — including agentic tools and
contracts — into a workflow that a Linux or macOS user reads and runs
unchanged. The hierarchy is fixed and does not change per OS:

1. **Bash# is the preferred authoring language.** The `.bsh` program owns
   control flow, data exchange, contracts (`@require`/`@ensure`/`@guard`), the
   `agentic` boundary and agentic orchestration. Public examples and the tour
   lead with Bash# calls on Windows, macOS and Linux alike.
2. **bashy is the execution system** on all three OSes: one static binary that
   provisions the guest runtime on demand, pinned and digest-verified, and
   never resolves a fence's toolchain from the host `PATH`.
3. **PowerShell and C# are callable islands inside a Bash# workflow.** They sit
   in the same island registry as Python, TypeScript, Rust, C, C++ and Go.
   They are where existing code and libraries are invoked; they do not become
   the shell, the pipeline or the orchestration layer. The shell pipe stays
   bytes; structured data crosses the boundary as a Bash# `Object`, never as a
   PowerShell object pipeline.

The operator's 2026-10-02 narrowing of the earlier ".NET refused" line makes
this admissible: the refusal stands for a CLR **linked or embedded inside
bashy itself**; a **guest-language fence backed by a separately downloaded,
pinned, on-demand toolchain** is admitted. No Microsoft binary is compiled
into, linked with, or vendored in bashy.

## Fence spellings

| canonical fence | aliases | unit |
|---|---|---|
| `powershell` | `pwsh`, `ps1` | a PowerShell script: its exported functions become callables |
| `csharp` | `cs` | a C# declaration unit: its exported `public static` methods become callables |

Both follow the existing island opener (`~~~<lang> [as <alias>]`), e.g.
`~~~powershell as ps` / `~~~csharp as cs`. The opener is already Class E
(handled by the polyglot fence grammar); adding two language names to the
registry needs no new grammar and no new collision-table row. A fence never
resolves its tool from `PATH` (Sprint 213 D1); both resolve through bashy's
toolchain resolver to the pinned PowerShell 7 runtime. Windows PowerShell 5.1
is **out of scope** (operator, 2026-10-02): the guest runtime is PowerShell 7.

## Value mapping at the boundary

Scalars and bytes cross as values; lists and maps cross as a Bash# `Object`
(JSON at the boundary), matching the island contract the other languages use.
No PowerShell object pipeline and no C# object graph crosses the boundary —
the conversion happens once, at the call edge.

| Bash# | → PowerShell param | PowerShell result → | C# param | C# result → |
|---|---|---|---|---|
| `string` | `[string]` | `string` | `string` | `string` |
| `int` | `[long]` | integral number | `long` | `long`/`int` |
| `float` | `[double]` | real number | `double` | `double` |
| `bool` | `[bool]` | `$true`/`$false` | `bool` | `bool` |
| bytes | `[byte[]]` | `byte[]` | `byte[]` | `byte[]` |
| `Object` (list) | array / `[object[]]` | array → list | `object[]` / `T[]` | array → list |
| `Object` (dict) | `[hashtable]`/`[pscustomobject]` | `pscustomobject`/`hashtable` → dict (JSON) | `IDictionary` / POCO | POCO/`IDictionary` → dict (JSON) |
| `null` | `$null` | `$null` → `null` | `null` | `null` |
| — | — | no return / `void` | `void` | no value |

The list/dict rows ride B1/B2/B3 (typed `Object` both ways); until those land,
a returned collection arrives as a JSON string, exactly as for the Python and
Rust rows today. Non-JSON-encodable values are refused at the call with a
diagnostic naming the argument.

## Calling a Verb-Noun function from Bash#

PowerShell's idiomatic function names are `Verb-Noun` (`Get-Item`,
`Invoke-Build`). **A hyphen is not legal in a Bash# call name** — in a
qualified call `ps.Get-Item(...)` the `-Item` is a minus, not part of the
identifier, and the name does not lower to a Go identifier. This must be
settled with a collision-table row and a Class R/E verdict **before any engine
code** (law of the repo; see `bashpp-posix-superset-syntax.md`).

Two spellings are on the table:

1. **String-keyed call (no new grammar) — recommended, Class-free.**
   `ps.call("Get-Item", path)` / `ps.call("Invoke-Build", target: "all")`.
   `call` is an ordinary island method taking the PowerShell name as a string,
   so stock `bash -n` sees a normal call — there is nothing new to collide
   with and no grammar change is required. This is the baseline spelling the
   first PowerShell row should ship.
2. **Hyphenated direct call (new call-name shape) — Class R, needs a row.**
   `ps.Get-Item(path)`. Stock bash rejects a `name-name(args)` call form (it
   is neither a valid function definition nor an expression), so the shape is
   **Class R (purely additive)**. It is admissible *only* with an explicit row
   in `bashpp-posix-superset-syntax.md`:

   | Start site in Bash# mode | Meaning / boundary | Shell escape or control |
   |---|---|---|
   | Qualified island call `alias.Verb-Noun(args)` where the name contains `-` | PowerShell function call; the hyphenated name is the PowerShell export, lowered via a deterministic transform (`Get-Item` ↔ `Get_Item`) recorded in a per-fence collision table | `ps.call("Verb-Noun", …)`; quote the word for shell |

**Verdict:** ship spelling 1 (string-keyed, no grammar impact) first; spelling
2 is **Class R** and may be added later, but only after its collision-table row
above is accepted and a one-to-one `Verb-Noun` ↔ call-name transform is
recorded so two exports can never map to the same Bash# call name. No engine
code precedes that row.

## C# fence body convention

A `~~~csharp` fence is a C# declaration unit, not a full program:

- **`using` lines** at the top of the body are permitted and carry into the
  generated unit.
- **Exported callables are `public static` methods.** Only `public static`
  methods become Bash# callables; instance methods, constructors and non-public
  members are internal to the fence.
- **One generated namespace per module.** Each fence (per source module) is
  compiled into a single generated namespace so method names do not collide
  across fences; the generated namespace name is derived from the module and is
  not author-visible.
- No top-level statements, no `Main`: the unit is a library of static methods.
- The accepted C# language version and the compiler diagnostics surface are
  fixed by the S358.1/S358.5 probe (`Add-Type` beside pinned `pwsh`, with the
  SDK fallback only if a required OS fails the probe) and recorded there, not
  guessed here.

## Error model

- **PowerShell.** A terminating error or an uncaught `throw` in the called
  function becomes a Bash# call failure; the `ErrorRecord` message and category
  are carried into the structured island error `{code,message,help}` (B4) and,
  at the command-adapter edge, into a non-zero exit and `weavecli.EnvelopeError`.
  Non-terminating errors written to the error stream are surfaced as the call's
  stderr and do not by themselves fail the call. The fence does **not** adopt
  `$ErrorActionPreference`, `-ErrorAction`, `ExecutionPolicy` or the two-kinds
  error model as Bash# semantics — errors cross as values with a stable code.
- **C#.** An uncaught exception from the invoked static method becomes a Bash#
  call failure carrying the exception type and message; compilation failures are
  reported with source-location diagnostics (file/line within the fence body)
  before the workflow runs, matching the Rust fence's prepare-time failure.
- Call stdout and stderr are preserved for both fences, as for the other
  islands. A hyphen/name-mapping collision (see above) is a prepare-time
  diagnostic, never a silent rebind.

## PowerShell command form (S358.4)

An exported PowerShell function is also a shell command: `alias.Name args`
in command position runs it with its streams wired into Bash# pipes and
redirections, the same command form the Python row has.

```
~~~powershell as ps
function Shout { process { $_.ToUpper() } }
function Rows  { [pscustomobject]@{ id = 1; name = 'a' } }
~~~
printf 'gamma\nalpha\n' | ps.Shout | sort
ps.Rows | jq -c .
```

- **argv.** Each word reaches the function as a positional `[string]`.
- **stdout.** The success stream is rendered once, at the boundary: a string
  is UTF-8 text plus `\n`; a `[byte[]]` is written byte for byte (no
  re-encoding, no added newline); a `Write-Host` record is its message; any
  other object is one line of compact JSON (`ConvertTo-Json -Compress`).
  The shell pipe stays bytes — no PowerShell object pipeline is added.
- **stdin.** A function that reads pipeline input — a `process` block, a
  `$input` reference, or a `ValueFromPipeline` parameter — runs as a filter
  over the command's stdin, one UTF-8 line per pipeline item (`\r\n` and `\n`
  both end a line). The stdin is read to EOF before the function runs. Any
  other function never reads stdin.
- **Line endings.** Text written to stdout and stderr is normalised to `\n`
  on every OS, so the same script produces the same bytes on Windows, Linux
  and macOS. Byte-array output is never normalised.
- **stderr.** Non-terminating errors (`Write-Error`) and warnings are written
  to stderr; the command keeps running. Verbose and debug streams are dropped.
- **Exit status.**

  | Outcome | Status | Streams |
  |---|---|---|
  | clean run | `0` | output on stdout |
  | non-terminating error / warning only | `0` | message on stderr |
  | native child exited non-zero (`$LASTEXITCODE`) | that code | as written |
  | terminating error / uncaught `throw` | `1` | message on stderr, stdout empty |
  | not an exported function of the fence | `127` | `command not found` |
  | worker died | `128+signal` (or its exit code) | — |

  `$LASTEXITCODE` is reset before each command, so a code never leaks from
  one command into the next. `set -e`, `&&`, `||` and `$?` see these
  statuses like any other command's.
- **The same error, two surfaces.** In a typed call
  (`value, err := ps.Fail()`) a terminating error binds to `err` with its
  message; in the command form it is status `1` with that message on stderr.

## Boundary: NuGet is a separate follow-up

This contract covers **fence source only** — PowerShell functions and C#
`public static` methods with the standard runtime/reference assemblies beside
`pwsh`. **NuGet package declarations are explicitly out of this contract** and
are a separate follow-up (Sprint 358 backlog story S358.6, `7455eb7e99dd`),
itself dependent on the optional .NET SDK work (Sprint 350 story 386,
`3f46d757ac67`). A fence body must not declare or restore NuGet packages under
this contract; adding that surface is a later decision with its own pin,
digest and licence inventory.

## What stays refused (unchanged by the 2026-10-02 narrowing)

The narrowing admits the guest-language fence and nothing else. Still refused,
unchanged: **an object pipeline in the shell**, **Verb-Noun renaming of POSIX
names**, **case-insensitivity**, **drive providers**, and **ExecutionPolicy**.
See the aligned refusal lists in `docs/sprint-215/powershell-study.md`,
`docs/sprint-215/bashsharp-feature-catalog.md` (rows B31/B32),
`docs/sprint-216-handoff.md` and `why-bashsharp.md`.
