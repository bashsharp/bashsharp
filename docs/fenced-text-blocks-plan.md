# Fenced text blocks in Bash# — decision + plan (catalog item B30)

**Status:** DELIVERED 2026-09-21 (Sprint 234, S.0–S.5). What delivery
settled beyond the decisions below: a command runner's stdout loses its
trailing newlines (as a command substitution reads it) while a Bash#
function runner returns its value as is; a denied verb binds the zero value
of each result so a `:=` site stays well-formed and reads status 126; every
text fence, row or runner, needs an alias (its methods are known only after
preparation); a `!runner` names a function declared anywhere in its unit
(the declaration is evaluated at prepare); built-in rows lower with the body
embedded, `dag`/`skill` and runner fences run interpreted only and lowering
refuses them by name (follow-up filed); a verb reads the caller's directory
and environment at call time; `kubectl` verbs are wired and provisioned but
were exercised only through the row's local `podman kube play` target.

## The question

Bash# has eight source fences — `~~~py`, `~~~ts`, `~~~rs`, `~~~c`, `~~~cxx`,
`~~~go`, `~~~bash`, `~~~sh` — each a *declaration unit* whose alias exposes
the functions the language analyzer finds in the body (`rs.launch()` in the
dag examples is a user-written `pub fn launch`, not a built-in verb). Should
the fence also carry a **text artifact** — a Dockerfile, an OpenTofu module, a
Kubernetes manifest, a Helm values file, a compose file, a dag, a skill —
whose alias exposes the *verbs of the processor* (`iac.plan()`,
`img.build()`, `ci.build()`), with the processor overridable per fence the way
a Unix magic number / shebang names the interpreter? And should the shell
detect the format on its own?

## Decisions

1. **Text fences — YES, as ONE generic runtime plus a table of rows, never a
   Go type per format.** A code fence *derives* its exports by parsing the
   body; a text fence's exports are *declared* (decision 2). Everything else
   is reused unchanged: alias binding, `alias.verb()` call resolution, the
   `(T, error)` bridge, artifact embedding when lowered, the Class-E start
   site, inertness under the Classic and POSIX dialects and under the
   certification profile. What makes it more than `cat > f && tofu apply`:
   - **contracts** — a world-changing verb (`apply`, `destroy`, `push`,
     `install`) carries an effect atom from the closed effect vocabulary, so
     `@guard` / `@effects` and a dag's `Effects:` cap it. Today contracts see
     only code-fence calls; the calls that touch the world are exactly these.
   - **self-provisioning** — the processor resolves through the fence tool
     resolver (the same seam every source fence uses), **never PATH**: podman
     is already embedded, kubectl and helm already have provisioners, tofu is
     one registry row (MPL-2.0 — the reason it is tofu and not terraform).
   - **byte-identical checkout** — the body materializes under the cache
     directory keyed by the plan id, never into the repository (the same rule
     `go build -overlay` enforces for Go fences).
   - **lowering** — the lowered program embeds the bytes (the Rust/C artifact
     model): one binary carries its infrastructure and its pipeline.
2. **Runner override — YES (the magic-number idea).** Grammar:

   ```
   ~~~<type> [as <alias>] [!<runner>]
   ```

   `~~~<type> as !<runner>` is sugar for `as <runner> !<runner>`. `<runner>`
   resolves, in order: a Bash# function in the same unit → a registered
   command of the host shell → **refusal**. Never PATH; wrapping a PATH tool
   in a function is the deterministic escape hatch. Calling convention:
   `runner <verb> <materialized-file> [args…]`; stdout is the result, a
   non-zero status is a call error; `BASHPP_FENCE_TYPE` and
   `BASHPP_FENCE_FILE` are set. **The runner declares its methods — nothing is
   guessed.** At prepare time the analyzer calls the reserved verb
   `runner methods <file>` and reads one export per line as JSON
   (`{"name":…, "signature":{…}, "effect":…}` — the existing export shape plus
   an optional effect atom). That list *is* the alias's export set:
   `alias.<undeclared>()` is a prepare-time unknown-callable error, and an
   empty or malformed answer is a refusal (`runner declared no methods`),
   never a fall back to dynamic dispatch. `effects` (a list) or `effect` (a
   comma-joined string) name the atoms. Built-in rows answer the same
   protocol from their table (the `dag` row answers it from the dag's own
   target list), and the declared effect is what `@guard` reads — built-in and
   custom alike. With a runner, `<type>` may be any identifier; without one,
   only the enumerated rows run. Lowering: an inline-function runner lowers
   with the program; a host-command runner needs the host shell on the target
   machine (recorded, not solved here).
3. **No auto-detection — for determinism, not feasibility.** The info string
   is the route, exactly as the dag runner and the polyglot canonical-language
   map key on it. What Bash# supports is the explicit table below — code and
   text — and nothing outside it runs without a `!runner`. YAML is the
   example: compose vs manifest vs Helm values is *stated*, never guessed.
   Extension-routed sugar (`import tf "./main.tf"`) is a later `import`-family
   item (B9), not part of the fence.
4. **`dag` and `skill` — YES, as rows registered by the host shell.** The
   engine never parses a dag: the row's processor is the host's own verb
   (`dag <file> <target>`, `skills run`), the same way `tf` → `tofu`. A Bash#
   script thereby carries its own CI/CD in the host's own task language, as
   it already carries a `~~~bash` island, while the engine module stays free
   of the dag package (the dependency direction rule). The rows live in the
   host shell behind an exported `RegisterLanguage`, the same pattern the dag
   runner uses for its interpreters. The types are `dag` and `skill` — the
   info string names the *format*, as `dockerfile` does; `~~~markdown as dag`
   would make the alias carry the route, which decision 3 forbids. Nesting
   works today: a fence closes on its own tilde run and the dag parser closes
   a body only on its opening marker, so ```` ```bashpp ```` ⊃ `~~~dag` ⊃
   ```` ```bash ```` nests; a body that itself contains `~~~` uses a longer
   outer run (the CommonMark rule; the recognizer accepts three or more).
5. **`compose` — design stub IN, implementation deferred.** The row is in the
   table and refuses by name until the embedded engine has a native path;
   `podman kube play` under the `k8s` row is the local target meanwhile.

`~~~tf as iac` is a user alias, so no verb named `iac` is introduced.

## The supported-format table

| kind | type (aliases) | processor | exports |
|---|---|---|---|
| code | `python` (`py`), `typescript` (`ts`), `rust` (`rs`), `c`, `cpp` (`cxx`), `go`, `bash`, `sh` | existing runtimes | parsed from the body |
| text | `dockerfile` | embedded podman | `build` → image id (`net,write`), `run` (`exec`) |
| text | `tf` (`tofu`, `hcl`) | `tofu` (registry row) | `validate`, `init`†, `plan` (`net,read`), `apply`† (`net,write,spend`), `destroy`† (`net,destroy,spend`), `output` |
| text | `k8s` (`kube`) | provisioned kubectl; `play`/`down` through podman (local) | `apply`†, `delete`†, `get`, `diff`, `play`†, `down`† |
| text | `helm` | provisioned helm, from the caller's directory | `template`, `install`†, `upgrade`†, `uninstall`† |
| text | `compose` | reserved — refuses by name | `up`†, `down`†, `ps`, `logs` |
| text | `dag` | the host's dag runner, from the caller's directory | its targets, each with its `Effects:` |
| text | `skill` | the host's skills runner over a private ring | `run`, `verify`, `probe` |
| any | `<ident> !runner` | a function in the unit or a registered command | whatever `runner methods <file>` declares |

† carries an effect atom; `@guard` without it exits 126. Every row, built-in
or custom, answers the same `methods` protocol — the table is the built-in
rows' answer written down.

## Plan

| story | scope | gate |
|---|---|---|
| **S.0 — this doc** | decisions + table; catalog row B30; grammar section in `bashpp-polyglot-fences.md` | index pointers updated |
| **S.1 — language registry** (behavior-preserving) | one `polyglot.Language{Canonical, Aliases, NewRuntime, NeedsEnvironment, Lookahead}` table with exported `RegisterLanguage`, replacing the two hardcoded runtime chains (interpreter + lowering), the canonical-name map, the environment-discovery whitelist and the parser's per-language lookahead switch | `go test -tags full ./polyglot ./interp ./lower ./syntax`; polyglot-gate rows byte-identical; classic gate OFF 86/86, ON 79 + 7 |
| **S.2 — grammar + generic text runtime** | `SourceBlock.Runner`; recognizer/parser/printer for `!runner`; `Export.Effect`; `polyglot.Text{Type, FileName, Tool, Verbs}` as analyzer (table or `methods` protocol) and runtime (materialize → exec per call, no worker); runner resolution order; lowered parity | unit tests with a fake tool and an inline-function runner; bridge and start-site rows in `bashsharp-tests` |
| **S.3 — rows `dockerfile` + `tf`** | tofu registry row + toolchain row; effect mapping documented | installed binary, scrubbed PATH, fresh cache: both rows run; `@guard` without the atom → 126 on `apply`, `plan` passes |
| **S.4 — rows `k8s`, `helm`, `dag`, `skill`; `compose` stub** | registered from the host shell (proves the seam); `skills run --path` if missing | `~~~dag as ci` + `ci.build()` runs the fenced target with its own `Effects:` enforced; `~~~skill as s` + `s.run()` |
| **S.5 — proof through the dag runner** | one example `dag.md` target with a ```` ```bashpp ```` body holding `~~~dockerfile as img` + `img.build()` under `Effects:`; one `.bsh` carrying a `~~~dag` island; `make smoke-dag-text` born self-provisioning | `smoke-dag-text` green on the installed binary with zero env; every existing `smoke-dag-*` lane stays green; the Bash 5.3 suite 86/86 before any tag |

S.1 pays for itself alone. S.2 is the language change; S.3–S.4 are rows;
S.5 is the gate. Later, on demand: `import tf "./main.tf"` (B9 family),
`~~~tf[workspace=prod]` attributes (B10 family), the compose implementation.
