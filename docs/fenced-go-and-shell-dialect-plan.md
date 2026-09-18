# Fenced Go and shell-dialect units in Bash++ — decision + plan (Sprints 191–193)

**Status:** DELIVERED 2026-09-15. Cards **#191** (engine), **#192**
(examples + gate), and **#193** (dialect islands) are complete in order.

## The question

Bash++ has five source fences — `~~~py`, `~~~ts`, `~~~rs`, `~~~c`, `~~~cxx` —
each proven through `bashy dag` against real repos (Sprints 181–190). Should
the set be completed with **Go**, with **bash / sh** (bashy's own embedded
interpreter, in its Classic / POSIX dialects), with **Bash++ itself**, and
should `dag.md` accept foreign bodies at the *target* level?

## Decisions

1. **`~~~go` — YES, Sprint 191 + 192.** It follows the one rule every fence
   follows — *the project's own toolchain, discovered from the checkout* —
   better than any existing fence:
   - the only foreign toolchain bashy already provisions (`bashy go`) and
     already needs (Bash++ lowers to Go) → no new self-containment debt;
   - the environment is the nearest `go.mod`, so a fence can `import` the
     checkout's own packages — the py/ts demonstration, on a native language
     (Rust/C fences cannot reach into the repo);
   - the toolchain rule is solved by Go itself (`go`/`toolchain` directives,
     `GOTOOLCHAIN`) — what rustup proxies do for Rust and nothing does for C;
   - `go build -overlay` places the generated worker `main.go` virtually
     inside the module, so the byte-identical `git status` rule holds with no
     file written into the checkout;
   - lowering: Day 1 uses the Rust/C model (compile a worker, embed the
     artifact) for parity; absorbing the fence's package into the lowered
     program in-process is a later fidelity step, not a redesign
     (Sprint 152: "a Go-only input lowers to itself").
   Bridge: exported top-level funcs; bool, integer and float kinds, `string`,
   `[]byte`; a trailing `error` result is a call error (Go's idiom, the
   `Result<T,E>` analogue); `encoding/json` frames. Selection: `BASHPP_GO` →
   PATH `go` → `bashy go`. Fingerprint: `go.mod`, `go.sum`, `go.work`,
   `GOFLAGS`. Analyzer: `go/parser` from the stdlib — no external AST step.
2. **`~~~bash` / `~~~sh` — YES, small and different, Sprint 193.** A
   *dialect island*, not a foreign worker: the unit runs on **bashy's embedded
   interp** in Bash-5.3 (`~~~bash`) or POSIX (`~~~sh`) dialect — never the host
   bash. It buys (a) verbatim shell from a repo behaving as bash, immune to
   Bash++'s Class-E start sites (the escape hatch the superset test needs), and
   (b) a certified-semantics island. Bridge: args → positional parameters,
   return = stdout, non-zero status = call error, isolated environment per call
   (a child interpreter, subshell-like), so "declaration unit, params in, value
   out" is unchanged. No process, no toolchain, no artifact. Inert in Classic
   and POSIX dialects like every fence.
3. **`~~~bashpp` inside Bash++ — NO.** Its only meaning is a namespaced unit
   (`lib.fn()`), i.e. modules. If namespaces are wanted, that is an
   `import`/package feature (Bash++ already has an explicit-package notion from
   the Go-corpus work); making "fence" mean both *foreign code* and
   *same-language module* blurs polyglot's one clean definition.
4. **Foreign bodies at the `dag.md` target level (` ```go ` as a whole recipe)
   — NO.** Sprints 185–190 showed the shell half is where `Effects:`, the
   cross-check and the exit code live. Nesting is already the answer: a
   ` ```bashpp ` body holding a `~~~go` fence works the day the engine does
   (the dag parser closes a body only on its opening marker). Sugar can come
   later on demand.

## Plan

| sprint | scope | gate |
|---|---|---|
| **191 — `~~~go` engine (sh)** | analyzer, module-aware worker via `-overlay`, `(T, error)` bridge, env fingerprint, `BASHPP_GO`, interpreted/lowered parity, Class-E start-site rows in `bashpp-tests` | `go test ./polyglot ./interp ./lower`; polyglot-gate Go rows; `tools/startsites/classify.sh --check` |
| **192 — fenced Go through `bashy dag`** | `bashy/examples/dag/{gh,hugo,caddy}/dag.md` (MIT / Apache-2.0 / Apache-2.0; `go.<project>()` imports the checkout's own package for its version, `run` launches the `go build` output); `make smoke-dag-go` **born self-provisioning** (pin + clone into `<UserCacheDir>/bashy/examples`, as `smoke-dag-c`) | `smoke-dag-go` on the installed binary with zero env; `smoke-dag-c` remains green |
| **193 — `~~~bash` / `~~~sh` dialect islands** | in-process child interpreter per call, stdout/status bridge, Class-E rows; one example in an existing `dag.md` (a repo's own shell helper pasted verbatim) | polyglot-gate rows + the example under its gate |

Order: 191 → 192 (the value); 193 is small and independent; the gate
housekeeping moved to dedicated Sprint 194 so it cannot collide with 192.
191 + 192 may run as one sprint under one seat
(the 187 → 188 split worked the same way). Rules carried forward: no
`--instruction` on `sprint start` when the external session is the conductor;
shell cross-checks with builtins only (#384); `env make` for GNU Makefiles;
bodies are PATH-only.
