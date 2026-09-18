# bash++ / dag as the narrow waist — tiered orchestration architecture

**Status:** architecture design of record (2026-07). Cross-cutting; spans bashy
(the language + local runner), outpost/mesh (sphere), and DKS/appstore (cluster).
Extends `docs/tessaro-fleet-execution-design.md` (venues as transports) and rests
on `docs/philosophy.md` / `bashy/docs/philosophy.md` (the narrow waist, local
first). The *language-spec* detail (bash++ grammar/builtins) is OSS and lives in
`bashy/`/`sh/`; this umbrella doc carries the vision and the tier integration.

## 1. Thesis — one authoring language, an executor per tier

Two facts decide the whole design:

1. **The winnable niche is the *local* tiers.** "local-first + pure-Go +
   zero-dependency + embeddable + a bash superset" is an *unoccupied*
   intersection (see the awesome-pipeline survey: the popular engines are either
   server-heavy control planes — Airflow/Prefect/Dagster/Temporal/Argo — or the
   right model in the wrong language — Nextflow/Snakemake on JVM/Python). Nobody
   owns pure-Go + local-first + fleet-aware. **We build there and win.**

2. **The distributed tiers are already owned by Apache-licensed incumbents.**
   Ray (sphere-class distributed compute) and Argo/Kubeflow/KubeRay (k8s
   workflows) are entrenched, huge, and permissive. Competing there is waste.
   **We integrate, we do not rebuild.**

The reconciliation is a **narrow waist**: a single authoring language —
**bash++**, structured into DAGs by **`bashy dag`** — spans all six execution
tiers, and a **compiler seam** lowers that one graph to the *tier-appropriate
executor*. Write the workflow once in bash++; the venue picks the engine:
in-process → `bashy podman` → a **Ray job** → an **Argo/Kubeflow/KubeRay**
manifest. Compat is the floor, the superset is the ceiling, the seam is the
elevator.

```
        authoring (ours, wins)                 execution (own local / integrate distributed)
   ┌───────────────────────────┐        ┌───────────────────────────────────────────────┐
   │  bash++   →   bashy dag    │  ──►   │ t1 in-process   t3 bashy podman                │
   │  (Go constructs, bash-     │ seam   │ t4 sphere: Ray                                 │
   │   compliant, zero deps)    │        │ t5 cluster: Argo / Kubeflow / KubeRay (DKS)    │
   └───────────────────────────┘        └───────────────────────────────────────────────┘
```

## 2. bash++ — Go constructs via bash-compliant extensions

**bash++ is bash 5.3 plus the ergonomics an agent needs, added so the base stays
a valid bash superset.** The design rule is absolute:

The normal `bashy` mode remains an exact Bash 5.3 implementation. Extended
grammar is enabled explicitly with canonical `--bashpp` or its exact
`--bash++` alias; it uses Go syntax directly and falls back to shell grammar
for ordinary commands, pipelines, expansions, and redirections. `--posix`
selects shell semantics independently, so `--posix --bashpp` and
`--posix --bash++` are supported POSIX-baseline Bash++ forms. Certification
uses `--posix` alone.

This makes Bash++ a language superset without requiring stock Bash to parse its
extended files. Go is the syntax and semantic reference because Go developers
should recognize the constructs without learning shell-specific substitutes.

### Go construct → bash++ realization

| Go construct | bash today | Bash++ |
|---|---|---|
| goroutine | `cmd &` | `go worker(arg)` |
| `defer` | `trap … RETURN/EXIT` | `defer cleanup(arg)` |
| typed values | strings, arrays | `type User struct { … }`, maps, selectors |
| modules | `source path` | Go-form `import` declarations |
| channels / `select` | FIFOs, coprocs | `make(chan T)`, `<-`, and `select { case … }` |
| explicit errors | `$?`, `set -e`, `\|\|` | `value, err := f()` followed by `if err != nil { … }` |

Go-shaped functions use explicit error returns. Ordinary shell commands retain
their exit status. The contextual parsing and compatibility contract live in
`docs/bashpp-posix-superset-syntax.md`.

**The elevator (hints).** When a script uses a construct bash++ can improve
(e.g. `cmd &` … `wait` where `go worker()` plus channels would add structured
parallelism), the agentic hint engine surfaces the upgrade — the same
"compat floor → superset ceiling → hint elevator" motion bashy already uses for
the coreutils verbs.

## 3. dag — the graph over bash++

`bashy dag` is the DAG structure *over* bash++ targets: each `###` target is a
bash++ body; `Requires:`/`Sources:`/`Effects:` are the edges + the effect cap;
`Attestation`/`ContextKey` are the governance (held-here-now, per
`bashy/docs/…skills…`). It is the **agent-first dogfood of Make** and the
local-first workflow runner (no server, no DB).

`dag-fanout` is its **fleet executor**: resource-weighted chunk assignment
(probe cpu/mem, weighted least-load) + per-host bounded concurrency + **retry of
infrastructure run-failures on an alternate host** (host drop / network / OOM
cost wall time, never results). This is the tier-1→4 executor and the reference
implementation of the seam's local end.

## 4. The compiler seam — one graph, tier-appropriate executors

The high-leverage piece to build is **the emitter**: lower a `dag` graph to the
executor a venue demands.

```
                         ┌── t1 userland  → in-process (Tier-1 pure-Go handler)
   dag graph (bash++) ───┼── t3 sandbox   → bashy podman (container per step)
     Requires/Sources/   ├── t4 sphere    → Ray job    (ray.remote tasks / actors)
     Effects/Attestation └── t5 cluster   → Argo Workflow · Kubeflow Pipeline · KubeRay
```

- **Local ends (t1/t3) already exist** — `dag`/`dag-fanout` run them directly.
- **`dag → Argo Workflow` (t5)** is the first emitter to build: a dag graph is
  almost 1:1 with an Argo `Workflow` (DAG template, `dependencies`, `retryStrategy`
  ← our failover, `podSpecPatch` ← resource weights). Highest ROI, smallest code.
- **`dag → Ray job` (t4)** is the second: scatter chunks as `ray.remote` tasks;
  Ray owns placement/retry across the pooled mesh. `bashy dag` *submits*; Ray
  *distributes*.

The seam is where our local-first authoring layer meets the entrenched engines —
small, and it is the whole "win *and* don't rebuild" trick.

## 5. Tier → engine mapping and the build-vs-integrate rule

| tier | engine | build / integrate | rationale |
|---|---|---|---|
| 1 userland · 2 workspace · 3 sandbox | **bash++ / `bashy dag`** (ours) | **build — and win** | local-first, bash superset, zero deps; a sandbox step is a dag step in `bashy podman`. One language, tiers 1–3. |
| 4 sphere (pooled compute / AI) | **Ray** | **integrate** as a mesh service | purpose-built distributed compute for pooling consumer machines for AI/LLM; Apache-2.0. |
| 5 cluster (DKS / k8s) | **Argo Workflows** (general DAG) · **Kubeflow** (ML pipelines) · **KubeRay** (Ray-on-k8s) | **integrate via appstore** (Helm pointers) | all k8s-native, Apache-2.0, installed as App Store entries — never vendored. KubeRay bridges sphere↔cluster. |
| 6 cloud | provider workflow services · Airflow (optional) | integrate | hosted edge; not core. |

### Verdicts on the surveyed candidates

- **Copy code from Dagger into `bashy dag`? No.** Apache-2.0 (legal to vendor)
  but its engine is **BuildKit — every step is a container behind a required
  daemon**, the opposite of a lightweight in-process tier-1 runner. `bashy dag`
  already gets containerized steps by dispatching to `bashy podman`. Dagger's
  only borrowable ideas are content-addressed caching + SDK ergonomics — not
  worth the coupling. If used at all, Dagger is an *optional sandbox/CI engine*,
  not a `dag` donor.
- **Airflow for sandbox/sphere? No — wrong layer.** Airflow is a central-scheduler
  ETL orchestrator (scheduler + DB + UI + Celery/K8s workers); it is not a
  compute-distribution substrate and does not fit p2p sphere. Its home is
  tier 5/6, and even there **Argo covers the k8s-DAG need more natively for DKS.**
  Keep Airflow optional/tier-6.
- **Ray for sphere? Yes** — the right tier-4 substrate; `bashy dag` orchestrates,
  Ray distributes. Do **not** rebuild a `dag-worker` to compete with Ray for AI
  jobs.
- **Kubeflow + Argo + KubeRay for cluster? Yes, via the appstore** — Apache-2.0
  Helm-pointer entries + a DKS install; the integration model already in place
  (download+install ≠ vendor).

### License posture

All the integration targets (Dagger, Airflow, Ray, Argo, Kubeflow, KubeRay) are
**Apache-2.0** — so bashy's supply-chain rule (`bashy/docs/licensing-supply-chain-policy.md`)
is satisfied by *integration*, not vendoring: the heavy distributed engines are
**download+install-and-run separate programs** (via the appstore / mesh
service), never bundled into a bashy binary. Only the pure-Go, local-first
`dag`/bash++ core is compiled in.

**TypeScript does not move this line** — see `typescript-go-toolchain.md`.
`microsoft/typescript-go` is Apache-2.0 and pure Go, so it is the first language
toolchain that could in principle be compiled in; but every package sits under
`internal/` and upstream declares the API "not ready", so it is unimportable
today and joins the *integrated* side as a provisioned tool. Its near-term value
is that it drops the Node runtime `tsc` currently requires. Worth revisiting as
a second target for this section's compiler seam only once upstream exports a
public API.

## 6. Why this is the winning shape

- **Own the waist.** Whoever owns the *authoring language* owns the workflow —
  and bash++/dag is the only authoring layer that is a certified bash superset,
  pure-Go, local-first, and fleet-aware. Agents already speak bash; bash++ is a
  zero-cost upgrade, not a new tool to learn or a runtime to install.
- **Delegate the substrate.** The distributed engines are commodities we route
  to, not products we clone. The seam keeps the switch cost near zero and lets
  the same workflow scale from a laptop (t1) to a pooled mesh (t4/Ray) to a DKS
  cluster (t5/Argo) without a rewrite.
- **Local-first is preserved.** The floor — the whole SDLC loop on one machine
  with no network — is untouched: tiers 1–3 need none of the integrated engines;
  they are the ceiling, reached only when the workload crosses the machine
  boundary.

## 7. Phasing

- **P0 — bash++ core** (`sh`/`bashy`): contextual `--bashpp` grammar, Go-form
  declarations/functions, explicit errors, `go`/`defer`, typed values,
  channels, and `select`. Brand-neutral, OSS.
- **P1 — `dag → Argo Workflow` emitter** (the first compiler seam; smallest, 1:1
  mapping, `retryStrategy` ← our failover).
- **P2 — `dag → Ray job` emitter** (sphere), submitted over the mesh.
- **P3 — appstore entries** for Argo Workflows / Kubeflow / KubeRay; DKS install
  path.
- **P4 — the hint elevator** for Bash++ upgrades when a script uses a
  plain-Bash equivalent.

## 8. Relationship to existing docs

- `docs/tessaro-fleet-execution-design.md` — the venue set this generalizes;
  `dag-fanout` is the local end of the seam.
- `docs/philosophy.md` + `bashy/docs/philosophy.md` — the narrow waist and
  local-first, which this operationalizes across tiers.
- `bashy/docs/bashy-skills-mechanism.md` §12a — the `dag Ensure:` ↔ dhnt-contract
  bridge; `use@version` reuses the versioned-artifact machinery.
- `docs/strategic-direction-2026.md` — the three-pillar direction this serves
  (LLM/agentic + DKS substrate + self-hosting foundation).
