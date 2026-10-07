# Bash# for agents: keep the action language familiar, make the boundary explicit

## Status at a glance

The historical measurements below are those recorded in [claims.md](claims.md)
as of **bashy v0.29.0 / `sprint-269`**; individual rows retain their stated
measurement versions. They are not a remeasurement of this candidate.

| area | status | evidence |
|---|---|---|
| Bash compatibility | **Shipped and measured.** With the dialect off, Bash#'s front door passed all 86 runnable GNU Bash 5.3 fixtures on Linux, macOS and Windows. This is a fixture result, not a claim of full Bash compatibility. | [claims.md](claims.md) |
| Polyglot islands | **Shipped and measured.** Python, TypeScript, Rust, C, C++ and Go islands can use provisioned toolchains instead of a host toolchain, downloaded and checksum-verified on first use. They are not bundled or offline; the rustup toolchain is `stable` at first install. | [claims.md](claims.md) |
| `agentic` | **Shipped and deliberately bounded.** It marks an action boundary plus an exit status; the interpreter does not call a model. | [claims.md](claims.md) |
| Guards | **Existing deterministic contracts; candidate experiment pending.** The current language documents `@require`, `@ensure` and `@guard`. The historical claim registry is not a test report for this candidate or evidence of agent-outcome improvements. | [Language contract](../README.md) |
| S5 shell-superset experiment | **Pending.** The planned paired experiment compares plain Bash, Bash# with guards, and Bash# with fences. Results, confidence intervals, costs and failure-taxonomy numbers will be added only from its reviewed evidence record. | [Experiment protocol](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/README.md) (publication planned this sprint); no result is claimed |

This page makes no host, account, deployment, or private-system claims. For
every shipped quantitative claim, use [claims.md](claims.md) as the source of
record.

## Why build on Bash?

An agent action language has to be something a model can write, something an
operator can inspect, and something that produces an execution result the
agent can use. Public research makes the case for retaining the shell surface
rather than asking models to learn a new DSL:

- In a 2026 controlled comparison, a Bash-only interface outperformed typed
  tool catalogs on TheAgentCompany and APEX-Agents while using fewer tokens;
  it also outperformed the study's restricted Python programmatic interface.
  The result is model- and benchmark-specific, not a universal ranking.
  [Is Bash All You Need?](https://arxiv.org/html/2609.11999)
- Code actions can be useful for multi-step composition, but the advantage is
  not invariant across models: an independent comparison found programmatic
  tool calling matched or beat JSON on 11 of 14 tested models and regressed on
  older models. [The Bitter Lesson of Tool Calling](https://arxiv.org/html/2608.06370v1)
- Boundary handling is a material shell risk: QuoteBench found that replaying
  the same command through one additional unescaped parser reduced success by 55.4–73.2
  percentage points. That directs work toward literal boundaries and
  verification rather than a replacement syntax. [QuoteBench](https://arxiv.org/abs/2608.13547)

These are external findings, not measurements of Bash#. They do not establish
that Bash# improves an agent outcome; that is the pending S5 question.

## What the superset is for

Bash# preserves the familiar shell as the orchestration layer: commands,
pipes, status, files and tools remain visible. Where a task benefits from a
different implementation language, a shipped fence can place that code in an
explicit island rather than turning the whole action interface into a new
runtime. The measured toolchain claim and its exact versions belong in
[claims.md](claims.md); this page does not extend it.

Existing deterministic guards and planned extensions must be distinguished.
The language documents `@require`, `@ensure` and `@guard`; the candidate's
experiment uses existing guards and fences. Its pending outcome is separate
from the historical measurements in [claims.md](claims.md). This page makes
no claim about additional guard or effect features, or a general safety
guarantee.

The historical [claim registry](claims.md) says that an `agentic` marker is a
boundary plus an exit status and that the interpreter does not call a model.
It also states that islands run with the task's host authority and that a
scratch image is not a sandbox. Those statements do not establish automatic
repair or per-action authorization for this candidate.

## How we will test the hypothesis

S5 is the evidence step. The public
[experiment protocol](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/README.md),
planned for publication this sprint, is the reference for the paired Bash,
Bash# guards and Bash# fences comparison.

The result is pending. Until the evidence record is reviewed, this page makes
no claim that any arm improves task success, cost, safety, or reliability.
When it arrives, the update will name the task count, confidence interval,
cost per solve, and failure taxonomy alongside the raw evidence.

## Read the evidence, not the slogan

- [Measured product claims and explicit non-claims](claims.md)
- [Shell-interface experiment](https://arxiv.org/html/2609.11999)
- [Programmatic-tool-calling comparison](https://arxiv.org/html/2608.06370v1)
- [Command-boundary study](https://arxiv.org/abs/2608.13547)
