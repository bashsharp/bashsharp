# Bash++ polyglot environments

**Status:** decision record, 2026-09-14. Applies first to Sprints 183 and 184.

## Decision

Bash++ owns environment **discovery, policy, identity and lifecycle**. Existing
ecosystem tools continue to own dependency resolution, installation and
language compatibility. Bash++ will not reimplement uv, pip, npm, pnpm, Yarn,
Bun, CPython, Node, or the TypeScript compiler.

The boundary is:

```text
Bash++ EnvironmentPlan
  ├── discover project metadata without executing it
  ├── select a named environment
  ├── select manager, compiler and runtime independently
  ├── verify lock/runtime state and compute an identity
  └── launch and supervise language workers

ecosystem manager  → materializes dependencies when explicitly requested
language runtime   → executes code
language compiler  → parses/checks/emits where applicable
```

For Python, uv/venv/Poetry/Conda/pip may prepare an environment, but CPython
executes the module. For TypeScript, npm/pnpm/Yarn/Bun may prepare dependencies,
the official TypeScript compiler checks the program, and Node or Bun executes
it. Bun may occupy both manager and runtime roles; those roles remain distinct
in the plan.

## Existing files are authoritative

Read-only discovery recognizes established project files rather than copying
their dependency data into a Bash++ format.

Python candidates, in decreasing specificity:

- `pyproject.toml` and its `requires-python`, project dependencies and tool
  tables;
- `uv.lock`, `poetry.lock`, `pdm.lock` and other recognized manager locks;
- `.python-version`;
- an existing configured virtual environment;
- `requirements*.txt` as a legacy fallback, never as a lock unless hashes and
  the selected provider make that guarantee explicit.

TypeScript/JavaScript candidates:

- `package.json`, including `packageManager`, `workspaces`, `type`, `exports`,
  `imports` and scripts;
- `tsconfig.json` and project references;
- `bun.lock`, `pnpm-lock.yaml`, `package-lock.json`, `yarn.lock` and recognized
  workspace locks;
- runtime configuration such as `bunfig.toml` or `deno.json` when the selected
  adapter supports it.

Discovery never runs package scripts, imports a module, evaluates configuration
code, contacts a registry, creates a virtual environment, or mutates a lockfile.
A manager declaration or lockfile is evidence about dependency management, not
by itself proof of the required execution runtime. In particular,
`packageManager: bun` does not silently force all TypeScript to Bun.

## Bash++ overlay

`bashpp.yaml` and `bashpp.json` are equivalent serializations of one versioned
schema. If both occur at the same project root, discovery fails as ambiguous.
The overlay binds names and policy to existing files; it does not duplicate
their dependencies:

```yaml
version: 1

environments:
  agent:
    language: python
    root: ./agent
    project: pyproject.toml
    manager: uv
    runtime: cpython
    lock: required

  opencode:
    language: typescript
    root: ./packages/opencode
    project: package.json
    compiler: typescript
    tsconfig: tsconfig.json
    manager: bun
    runtime: bun
    lock: required
```

The generic name `project.yaml` is deliberately not claimed: it is likely to
collide with unrelated tools and says nothing about which system owns the file.
Future dhnt-wide composition may reference this schema, but Bash++ keeps a
recognizable filename.

Environment selection must support names because one source unit may eventually
use multiple Python versions or both Node- and Bun-backed TypeScript. Proposed
source syntax is illustrative until its parser story is accepted:

```bash
import python[agent] "minisweagent.agents" as agents
import typescript[opencode] "opencode/config/paths" as paths
```

An omitted environment name uses the one unambiguous environment for that
language. Zero or multiple candidates produce an actionable decision report,
not a guessed selection.

## Precedence and roots

The resolved plan uses this precedence, highest first:

1. explicit source-level named environment;
2. explicit Bash++ invocation override;
3. `bashpp.yaml` or `bashpp.json` binding;
4. recognized ecosystem project and lock metadata;
5. an explicitly inherited active environment or executable on `PATH`.

Discovery starts from the Bash++ source location, not an incidental process
cwd, and walks toward a declared workspace/VCS boundary. An explicit project
root terminates discovery. Nested projects are separate candidates; crossing a
boundary or choosing among conflicting locks requires an explicit binding.

Environment variables such as `BASHPP_PYTHON`, `BASHPP_NODE`, `BASHPP_BUN`,
`BASHPP_TYPESCRIPT_MODULE` and project-root overrides remain supported as
invocation overrides. They are normalized into the same `EnvironmentPlan`
rather than creating hidden alternate execution paths.

## Immutable EnvironmentPlan

The language engine consumes an immutable plan containing at least:

- environment name and language;
- canonical project/workspace root;
- source manifest and lockfile identities;
- dependency manager kind/version;
- compiler kind/version when applicable;
- runtime kind, executable identity and language version;
- selected configuration files and resolution conditions;
- inherited environment inputs that affect module resolution;
- lock policy and a content fingerprint;
- a redacted explanation of every selection decision.

The fingerprint covers canonical configuration content, lock content, runtime
and compiler identity, platform/architecture, relevant resolution inputs and
Bash++ adapter version. It keys analysis artifacts and worker pools. Secrets,
tokens and the entire ambient environment are neither logged nor hashed
indiscriminately.

The plan is observable through a future read-only surface such as:

```bash
bashy env inspect
bashy env plan --name opencode
```

Its output reports what was selected and why, including ambiguity, missing
runtime, dirty/unlocked dependency state and the fingerprint.

## Provisioning is explicit

Normal parsing, checking, compilation and execution do not install or update
dependencies. Provisioning is a separate governed operation, for example:

```bash
bashy env sync --name agent --locked
bashy env sync --name opencode --locked
```

The provider adapter translates that request to the selected ecosystem tool
and records the command/effects. Lock-required mode refuses an absent, stale or
mutated lock. Network access, Python build backends and JavaScript lifecycle
scripts are material effects requiring the same preflight/policy boundary as
other Bash++ operations. No provider receives a silent `--force`, lock rewrite,
global install, or fallback to a different manager.

Initial Sprints 183 and 184 implement read-only discovery, selection,
fingerprinting and worker launch only. `env sync` is a named follow-up unless a
separate reviewed story adds its mutation, locking and policy gates.

## Ownership across Sprints 183 and 184

Sprint 183 owns the language-neutral `EnvironmentPlan`, deterministic discovery
and Python projection. Sprint 184 consumes that exact substrate and adds only
TypeScript project/compiler plus Node/Bun projections. It must not build a
second manifest scanner, precedence stack, fingerprint, cache key, or CLI.

Both sprints retain direct executable overrides for hermetic tests and unusual
installations. Their unchanged third-party fixture gates record the complete
resolved plan so a passing result can be reproduced.

## Deferred scope

- dependency solving or a Bash++ lockfile format;
- automatic installation during import or execution;
- global package installation;
- remote environment registries and distribution;
- container/sandbox materialization;
- secrets embedded in environment plans;
- environment activation by shell mutation;
- hot switching a live handle between environments;
- claiming compatibility based only on manifest recognition.

These can build on the immutable plan later without changing the rule that
ecosystem tools remain the compatibility authorities.
