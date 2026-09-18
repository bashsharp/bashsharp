# Bash++ automation shortcomings exposed by the POSIX campaign

Status: **post-certification design input, 2026-08-27.** This note records
lessons for the Bash++ Go-feature workstream. It does not propose rewriting the
VSC-PCTS harness, and it does not change the POSIX certification configuration.
Certification remains the immediate priority; these items are intentionally
deferred until that campaign is complete.

## Why this note exists

Preparing the Profile D certification environment took disproportionate effort
even though the intended workflow was straightforward. Shell was not the only
cause—external tools, operating-system behavior, and the licensed suite also
contributed—but ordinary shell semantics amplified small mistakes and delayed
their discovery.

The important product lesson is not “replace the certification scripts.” It is
that Bash++ should make long-lived automation easier to validate, compose,
resume, and review while preserving Bash and POSIX compatibility when Bash++ is
off.

## Shortcomings observed

1. **Syntax validation is too shallow.** A script can be valid shell while an
   embedded `awk`, `sed`, or command expression uses an unsupported dialect.
   These errors appear only when execution reaches the line.
2. **Ambient process state is invisible.** Forwarded `LC_*` variables changed
   the behavior of utilities inside an otherwise controlled run. Shell does not
   make a function's environment inputs explicit.
3. **Commands are stringly typed.** Executable identity, arguments, paths,
   versions, checksums, exit status, stdout, and stderr are passed as loosely
   related strings. Word splitting, globbing, and quoting remain latent hazards.
4. **Human output becomes an accidental API.** Parsing `systemctl` display
   output caused a false positive when headers and footers were mistaken for
   unit rows. Similar pipelines silently lose schema and provenance.
5. **Failure semantics are contextual and surprising.** `set -e`, pipelines,
   substitutions, conditionals, and traps do not form a simple error model.
   Expected nonzero status and ignored failure are difficult to distinguish in
   review.
6. **Workflow state is ad hoc.** Idempotence, phase completion, resumability,
   required inputs, produced artifacts, and invalidation are manually encoded
   in marker files and strings.
7. **Effects and dependencies are implicit.** A script does not declare that a
   task needs network, root, a locale, a command provider, a mount, or a specific
   artifact. Missing dependencies are often discovered late.
8. **Cleanup is fragile.** Traps help, but ownership and ordering become unclear
   across functions, subprocesses, concurrent jobs, and partial failure.

These are language and runtime shortcomings, not evidence that shell is
intrinsically unsuitable for all automation. Shell remains excellent at
interactive composition and process orchestration. Bash++ should preserve that
strength and add structure where automation needs it.

## Bash++ capabilities to explore

The existing Bash++ design already provides the foundation: `LangBashPP`,
native object values, Go-shaped types, explicit `(value, err)` returns,
`defer`, imports, and structured concurrency. The implementation phase should
also explore the following contracts.

### 1. A semantic check/build phase

Add a command such as `bashy check` that parses the entire Bash++ unit and its
declared imports before execution. It should validate names, types, return
values, unreachable or unchecked errors, task dependencies, effect
declarations, and supported embedded-language dialects where Bashy owns the
interpreter.

“Build” need not mean emitting a native executable. A checked, immutable Bashy
IR or cached execution plan is sufficient. The key property is that structural
mistakes fail before a remote host is provisioned or a destructive step runs.

### 2. Typed command invocation and results

A structured invocation should preserve an argument vector without implicit
word splitting or glob expansion and return a value such as:

```go
type CommandResult struct {
    Stdout string
    Stderr string
    Status int
}
```

Ignoring a nonzero status should be explicit. Ordinary shell commands and `$?`
must remain available, but checked Bash++ code should be able to require that
every error is handled.

### 3. Hygienic environment scopes

Explore a typed, lexically scoped environment value or block that can start
clean, inherit selected names, set required values, and explicitly unset locale
categories. A task's environment should be inspectable before it runs and
restored automatically afterward.

This would make requirements such as `LANG=POSIX` and “no inherited `LC_*`” a
declared input rather than repeated shell ceremony.

### 4. Structured process boundaries

Prefer native objects inside Bashy and schema-aware JSON at `execve` boundaries,
as the existing L0 design proposes. Add typed decoders and adapters for machine
formats instead of encouraging parsers for human-readable output. If a command
has no structured interface, the conversion from text should be explicit and
validated.

### 5. Contracts, tasks, and resumability

Bash++/`bashy dag` should be able to declare:

- required inputs and their content identities;
- produced artifacts and postconditions;
- effects and capabilities;
- retry and cleanup ownership;
- durable completion state; and
- invalidation rules when code, inputs, environment, or providers change.

A completed task should be reusable only when its declared contract still
matches. This turns restartability from convention into runtime behavior.

### 6. Provider and path identity

Executable resolution should optionally return a typed provider identity—not
only a path string—including whether the command is an in-process applet,
shell builtin, or managed external and which version/digest backs it. Tasks can
then assert the measured command surface without scraping `which` output.

### 7. Explicit effects and unsafe escape hatches

Network, filesystem mutation, privilege changes, process launch, signals, and
secret access should participate in Bashy's existing effect/capability model.
Dynamic legacy shell remains essential, but unchecked evaluation or text-based
escape should be visually explicit so reviewers know where static guarantees
end.

## Example direction, not settled syntax

The exact grammar remains owned by the Bash++ design of record. Conceptually, a
checked task should be able to express this information:

```go
task prepare {
    effects := []Effect{Network, FilesystemWrite, Privilege}
    env := CleanEnv().Set("LANG", "POSIX")
    require(tool("awk").Provider == GoApplet)

    result, err := run(env, []string{"make", "test"})
    if err != nil {
        return err
    }
    ensure(result.Status == 0)
    produce("build-manifest", sha256("build-manifest.txt"))
}
```

This example is deliberately semantic rather than normative. It must not be
used to bypass the compatibility, exact-Go-spelling, process-boundary, or
superset decisions in the existing Bash++ specifications.

## Acceptance criteria for the future workstream

The Bash++ implementation should demonstrate that:

1. ordinary Bash/POSIX behavior is unchanged when Bash++ is disabled;
2. the permanent Bash++ superset differential gate remains byte-identical;
3. `bashy check` catches representative environment, dependency, type, error,
   and task-graph mistakes before execution;
4. typed invocation cannot accidentally resplit an argument vector;
5. command provider/version identity can be asserted without parsing display
   output;
6. interrupted tasks resume only when declared inputs and effects permit it;
7. interpreted and compiled/cached forms have identical observable behavior;
   and
8. the race, cancellation, cleanup, and leak gates required by the existing
   Bash++ design remain green.

## Relationship to existing design

- `bashy/docs/bash-plus-plus-design.md` defines the native-value/runtime phases.
- `docs/bashpp-posix-superset-syntax.md` owns grammar, activation, compatibility,
  and Go-fidelity decisions.
- `docs/bashpp-dag-tiered-orchestration.md` owns task graphs, effects, and the
  executor/compiler seam.

This note supplies observed automation requirements to those designs. It does
not supersede them.
