# Why Bash#, and for what

The page to read before adopting it — and the page to hand to whoever has
to approve that. Every number here is copied from [claims.md](claims.md);
every snippet is copied from a passing case in the
[tour](https://github.com/bashsharp/tour). Bash# is **alpha**: what is
shipped is labelled shipped, what is planned is labelled planned, and
nothing is called "coming" unless it runs from the current release.

## The one-screen version

**Bash#** is the bash you already know, Go where you need types, any fenced
language where you need a library, and `agentic` where you need a model —
with contracts so a model's output is judged, never trusted. It runs inside
[bashy](https://github.com/qiangli/bashy), a pure-Go Bash 5.3 for Linux,
macOS and Windows. Four properties carry the case:

1. **The same file means the same thing on every OS.** One static binary per
   platform; bash's own 86-fixture suite passes 86/86 with the dialect off;
   and a fenced island *never* takes its toolchain from the host's `PATH` —
   bashy provisions Go, `zig cc`, CPython, Node and rustup itself, pinned
   and checksum-verified — so a script's meaning does not depend on what
   happens to be installed. The tour's CI proves it on Linux, macOS and
   Windows twice per OS: once with the runner's toolchains on `PATH`, once
   with all of them stripped off, identical transcripts.
2. **Production is one static binary plus your script.** bashy is built
   `CGO_ENABLED=0`; the `bash` build is a 5.6–6.3 MB release archive, the
   full `bashy` 46–50 MB (v0.24.0, compressed). No distribution base image,
   no `apt`, no runtime layers you never call; the coreutils are Go applets
   inside the binary. For audit that means one Go module graph
   (`go version -m bashy` is the SBOM), downloads that fail closed without a
   pinned digest, and no host `bash`/`coreutils` to be injected through.
3. **The best language for each sub-task, in one file.** A `~~~python`,
   `~~~typescript`, `~~~rust`, `~~~c`/`~~~cpp`, `~~~go` or `~~~bash` block
   becomes an ordinary callable — `got := py.main()` — with typed values
   crossing the boundary, run by a real process in the project's own
   environment. No `python -c` quoting, no wrapper script, no rewrite.
4. **A model's output is judged, never trusted.** `agentic` is the language's
   `unsafe`: the one lexical place a program may hand work to a model.
   `@require`/`@ensure` are shell commands run deterministically around it,
   `@guard(effects:)` is an enforced cap, and status 6 is a defined *yield*
   ("I need input") that reaches whoever ran the script. The interpreter
   itself never calls a model.

And the sentence that gets alpha software approved: **you can leave.**
`bashy transpile --bashsharp file.bsh -o file.go --standalone` lowers the
whole file to ordinary Go with its own `go.mod`; if it grows past a script,
you keep a Go program, not a dependency.

What it is not: not "100 % Go" (an enumerated profile, 2,827 of 3,497
upstream roots pass both modes); not POSIX *certified* (493/493 on the
licensed shell arm is a pre-flight); not fast (≈ 2× slower than GNU bash on
microbenchmarks); islands are not a sandbox (they run with your
permissions). The current candidate passes all 86 runnable Bash 5.3 fixtures
on Windows, Linux, and macOS; that fixture result does not establish complete
interactive Bash compatibility ([claims.md](claims.md)).

## When to use it — ranked

### 1. Scripts that must behave identically on Linux, macOS and Windows

*Replaces:* a PowerShell/bash split, WSL, Git-Bash, "works on my machine".

```bash
#!/usr/bin/env -S bashy --bashsharp
func greet(name string, retries int = 3) {
    printf '%s:%d\n' name retries
}
greet("Ada")                    # default applied
greet(retries: 7, name: "Cy")   # keywords, any order
```

Shipped: the pure-Go engine, the applets, the provisioned toolchains, the
three-OS tour gate. Delivered on a release candidate not yet in that tour
gate (`sh 3ee22f2`, `yoke da6d602`, `bashy 0f73f42`): a first hour of
Windows mechanisms — shared path conversion, `wslpath`/`cygpath`, `pwd -W`,
drive-letter cwd, special operands, in-process pipes, named-pipe process
substitution, declared path/list environment conversion via `BASHYENV`, and
a register fix. That first candidate measured 23/86 on Windows. Later Sprint
253 production pins passed 86/86 on Windows and Linux, and the Sprint 257
timezone candidate passed 86/86 on two Windows builds, native macOS, and two
Ubuntu test droplets ([claims.md](claims.md)). Planned: `C:\x` = `/c/x` =
`/mnt/c/x` equivalence in every applet.

### 2. Deploying automation without a base image

*Replaces:* an `ubuntu`/`alpine` image carrying bash + coreutils + python for
a 40-line script.

Which shapes are honestly one artifact today:

| what the script uses | the deployable | one artifact? |
|---|---|---|
| bash, or bash + the Go-typed core, interpreted | `bashy` + the `.bsh` | yes — a `FROM scratch` image with two files. Ten POSIX names (`m4 man ctags ar nm strip ex vi lp localedef`) are pinned external providers built from source at install, not applets; if the script calls one, ship it |
| `bashy transpile --standalone` → `go build` | one Go binary, no bashy | yes |
| compiled islands (`rs`/`c`/`cxx`/`go`), transpiled | the worker is embedded in the Go program | yes |
| compiled islands, interpreted | needs the provisioned `rustc`/`zig cc`/`go` at first call | no — pre-provision, or transpile |
| Python / TypeScript islands | a pinned CPython / Node provisioned at first use | no — `bashy check --prepare` at image build bakes it in; two layers, still no distro |

Say "smaller *supply-chain* surface", not "sandboxed": an island runs with
the task's host authority, and the ten pinned external providers named
above still apply if a script calls one. Measured, not roadmap: three
`FROM scratch` images for the three Sprint 216 delivery shapes, built from candidate
`bashy 0f73f42` and run green — row 1 (bashy + `.bsh`) 109,412,490 B /
49,502,560 B gzip; row 2 (`transpile --standalone` binary) 1,994,912 B /
876,537 B gzip, SBOM `mvdan.cc/sh/v3 v3.13.1`; row 3 (bashy + a Python
island with CPython prepared at build time and no runtime download)
166,720,052 B / 68,927,404 B gzip
([claims.md](claims.md)).

### 3. The best language per sub-task, in one file

*Replaces:* a bash script orchestrating N interpreters with N quoting, venv
and `PATH` problems; a notebook that cannot be run from CI.

```bash
#!/usr/bin/env -S bashy --bashsharp
~~~py as py
def major() -> int:
    import sys
    return sys.version_info[0]

def shout(s: str) -> str:
    return s.upper() + "!"
~~~

v := py.major()
echo "python major version: $v"
s := py.shout("islands")
echo "$s"
```

The same shape holds for a `~~~go as g` block compiled against the nearest
`go.mod` — the project's own — and for TypeScript, Rust, C/C++ and bash.
bashy's `examples/dag/` has eighteen real repositories (ffmpeg, curl, git,
llama.cpp, opencode, codex, uv, bun, gh, hugo, caddy, …) whose smoke target
calls the project's own code in its own language this way; one of them mixes
Python and TypeScript in a single body.

Shipped: seven island languages; scalars, bytes and opaque handles cross the
boundary. Planned: lists and dicts as first-class values on both sides
(today a returned list arrives as a JSON string), an island function as a
pipeline filter reading stdin, Rust on the same persistent worker the other
islands use, `import python "./file.py"` by path.

### 4. Automation an agent runs — with a deterministic judge

*Replaces:* trusting whatever the model wrote into `bash -c`; an LLM as the
judge of its own output.

```bash
#!/usr/bin/env -S bashy --bashsharp
@ensure('echo "ensure ran for $1" >&2; true')
agentic function step() {
    case "$1" in
    ask) return 6 ;;
    esac
    echo "did $1"
}

agentic {
    step build; echo "build -> $?"
    step ask || exit $?        # 6 reaches whoever ran the script
    echo "not reached"
}
```

Precondition failed → 3, the body never ran. Postcondition disagreed → 3.
A `touch` inside `@guard(effects: "read")` → denied. Yield → 6, and no
`@ensure` runs on a yield, so a harness never sees "postcondition failed"
masking "input required". The quickstart (`judge.bsh`) shows all six exit
codes offline, no API key.

Shipped: the scope rule, contracts, the enforced guard, the yield, decorators
and advice. Planned: attestation records for functions and `agentic {}`
blocks (today: dag targets and skills only); the canonical written form of
an `agentic` action across files; what each coding harness does with exit 6
— an open RFC.

### 5. Judge a model-written script *before* running it

`bashy check --bashsharp file.bsh` for static checks, `bashy dag --explain`
for a dry plan, contracts that refuse before the body runs — none of it
needs a harness to cooperate. Shipped, with one caveat: the null-safety
check is alpha and today only fires on a file that is nothing but
declarations.

### 6. Build, test and smoke as a Markdown task graph

`bashy dag` reads `### target` headings from a `dag.md`: content-hashed
up-to-date checks instead of mtimes, `Require:`/`Ensure:`/`Effects:` lines,
`--json` envelopes with stable exit codes, `-j`, and `--mesh` to run a body
on another host over ssh. Shipped. Planned: `Effects:` is declared and
recorded but not yet enforced — for pure task running with no contracts,
`just` is the honest recommendation until it is.

### 7. The script that outgrew bash but does not deserve a rewrite

Put the ten lines of Go in the shell script: typed `func`, structs,
generics, keyword and default arguments, exhaustive enums, deep `readonly`.
Shipped. Planned: a top-level Go `if`/`for` block between shell commands
(today it must sit inside a `func`); a `(T, error)` return in shell text;
goroutines in mixed text (whole Go programs already have them).

## Compared to

Honest wins and honest losses, one paragraph each.

- **Plain bash.** Everything bash does, unchanged — 86/86 on its own suite —
  plus types, islands, contracts and Windows. Loses: ≈ 2× slower.
- **Go.** Bash# is the on-ramp and `transpile` the off-ramp: a file that is
  80 % shell stays a file; if it grows, you end up in Go by design. If you
  are writing Go anyway, write Go.
- **Python + `subprocess`.** Wins on quoting (an island is the fourth option
  after `python -c`, a wrapper script and a rewrite), on a judge at the
  process boundary, and on the deploy footprint. Loses on the library
  ecosystem — which is exactly what islands are for.
- **Make / Just / Task.** `dag.md` wins on content-hashing, pinned toolchains
  and `Require:/Ensure:`; while `Effects:` is unenforced, Just is the honest
  pick for pure task running, and Make is everywhere.
- **YSH / Oil.** A cleaner language, but a new dialect: existing scripts
  must change. Bash# adds Go's type system rather than inventing one, and
  runs unchanged scripts.
- **Nushell.** Structured pipelines are genuinely better for data wrangling;
  use it for that. Bash# keeps `|` as bytes — structured data lives in
  typed values and `--json` envelopes — so every existing script still
  runs, and Rust can live inside a script as an island.
- **xonsh.** The closest sibling. Bash# does not guess Python-versus-shell
  per line (in xonsh, binding `ls = 1` turns `ls -l` into subtraction) and
  does not run Python functions on threads inside the shell; Python lives
  where you put it, as an island with a real pid. Loses on Python
  ergonomics at the prompt.
- **PowerShell.** On every Windows box, with Windows administration
  (services, event log, registry, certificates) Bash# does not attempt.
  Bash# wins on being bash, on one static binary with no .NET, and on
  effects declared for *every* command where `-WhatIf` covers only cmdlets
  that opted in.
- **zx / Bun / Deno scripting.** Fine glue for a JavaScript team. Bash#
  needs no runtime on the host and has contracts; loses for teams already
  living in TypeScript.
- **Nix.** Strictly more reproducible and hermetic. Bash# is "pinned
  toolchains for the tools you actually call", one binary, no new language,
  and it runs on Windows.
- **Notebooks.** Exploration versus a deployable, versionable script; Bash#
  islands are the scripted form of the same idea.

## When not to use it

- You need structured pipelines between arbitrary commands — Nushell.
- You need Windows administration cmdlets — PowerShell.
- The library is the whole job — Python, with an island only if a shell
  wrapper genuinely helps.
- You need hermetic builds — Nix.
- Startup or throughput is the constraint — GNU bash is ≈ 2× faster.
- You need a sandbox — islands run with your permissions; put bashy inside
  one.

## What is planned, by use case

Nothing here is promised for a date. Each item names its status.

| use case | planned item | status |
|---|---|---|
| same file every OS | bash's 86-fixture suite on Windows, macOS, and Linux | 86/86 on the current measured candidate ([claims.md](claims.md)) |
| | closing the historical Windows fixture gap | delivered in Sprint 253; Sprint 257 timezone follow-up verified |
| | `wslpath`/`cygpath`, `pwd -W`, drive-letter cwd, special operands, in-process pipes, named-pipe process substitution, declared path/list env conversion via `BASHYENV` | delivered on a candidate (`sh 3ee22f2`, `yoke da6d602`, `bashy 0f73f42`) |
| | `C:\x` = `/c/x` = `/mnt/c/x` equivalence in every applet | filed |
| no base image | a worked `FROM scratch` example with measured size | measured on a candidate ([claims.md](claims.md)) |
| one file, many languages | list/dict values both ways; island function as a pipeline filter; Rust on the persistent worker; `import python` by path | filed |
| judged, never trusted | attestation for functions and `agentic {}`; the canonical `agentic` written form; exit-6 handling per harness | open RFC |
| task graph | `Effects:` enforced | filed |
| outgrew bash | top-level Go `if`/`for` in shell text; `(T, error)` in shell text; goroutines in mixed text | planned |
| Go corpus | the 284 repairable roots | in progress, by ID in [go-corpus-targets.md](go-corpus-targets.md) |

Refused, permanently — so nobody waits for them: list comprehensions,
ternary, `match`, `try`/`catch`, operator and method overloading, classical
inheritance, `async`/`await`, `?.`/`??`, an object pipeline in `|`,
case-insensitive names, backtick line continuation. The reasons are in
[bashsharp-ergonomics-tier.md](bashsharp-ergonomics-tier.md): a construct
that cannot lower to ordinary Go splits the language.

## Where the numbers come from

[claims.md](claims.md) — every figure with its corpus, revision and host;
what is not claimed; how to reproduce each row. If a sentence on this page
carries a number that is not there, the sentence is wrong.
