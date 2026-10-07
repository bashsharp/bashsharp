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
| Guards | **Existing deterministic contracts; measured configuration regressed.** The current language documents `@require`, `@ensure` and `@guard`. The historical claim registry is not a test report for this candidate or evidence of agent-outcome improvements. | [Language contract](../README.md) |
| S5 shell-superset experiment | **Measured negative result; independent review pending.** Bash solved 59/60, guards 13/60 and fences 12/60. See the bounded comparison below. | [Experiment protocol](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/README.md) and [scalar results](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/results/sprint-381.json) |

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
that Bash# improves an agent outcome; the S5 configuration result below shows no benefit.

## What the superset is for

Bash# preserves the familiar shell as the orchestration layer: commands,
pipes, status, files and tools remain visible. Where a task benefits from a
different implementation language, a shipped fence can place that code in an
explicit island rather than turning the whole action interface into a new
runtime. The measured toolchain claim and its exact versions belong in
[claims.md](claims.md); this page does not extend it.

Existing deterministic guards and later extensions must be distinguished.
The language documents `@require`, `@ensure` and `@guard`; the candidate's
experiment uses existing guards and fences. Its measured outcome is separate
from the historical measurements in [claims.md](claims.md). This page makes
no claim about additional guard or effect features, or a general safety
guarantee.

The historical [claim registry](claims.md) says that an `agentic` marker is a
boundary plus an exit status and that the interpreter does not call a model.
It also states that islands run with the task's host authority and that a
scratch image is not a sandbox. Those statements do not establish automatic
repair or per-action authorization for this candidate.

## What the paired experiment measured

**This configuration showed no benefit: both treatment arms solved substantially
fewer trials than the dialect-off baseline. The final evidence has passed
independent review; these figures are not a release gate.** The public
[scalar evidence](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/results/sprint-381.json)
and [protocol](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/README.md)
record 20 tasks, K=3 repetitions: 60 valid trials per arm. All arms used
`gpt-6-luna`, low reasoning effort, Codex CLI 0.157.1 and the same 60-second
budget. The baseline is the same Bashy candidate with `--no-bashpp`,
not a separate GNU Bash binary. Of 184 attempts, four infrastructure voids remain in the history;
180 valid outcomes enter the comparison. Valid outcomes were not selectively
rerun, and timeouts count as failures.

The frozen candidate binary SHA-256 is
`52e67841c1972dcb1d447ebf9271cd1037200adc2644877746267fe8b2a25040`.
This benchmark did not include the S7/S8 fleet rows and says nothing about
whether the S8 full harness passed.

| arm | solves / valid trials | success | known tokens / solve, lower bound | unknown usage rows | trials with effect-cap denial |
|---|---:|---:|---:|---:|---:|
| Bash, dialect off | 59/60 | 98.33% | 84,486.97 | 0 | 0 |
| Bash# guards/contracts | 13/60 | 21.67% | 411,259.46 | 8 | 53 |
| Bash# fences | 12/60 | 20.00% | 489,490.50 | 8 | 47 |

| paired success-rate difference | estimate (percentage points) | paired normal 95% CI (percentage points) |
|---|---:|---:|
| Guards minus Bash | −76.67 | [−88.42, −64.91] |
| Fences minus Bash | −78.33 | [−88.85, −67.82] |
| Fences minus guards | −1.67 | [−16.02, +12.69] |

These normal-approximation intervals pair **60 task-repetition outcomes**;
they do **not** cluster the three repetitions by the 20 tasks. They therefore
do not account for within-task dependence, and should not be read as
cluster-adjusted uncertainty or a generalization to other tasks or models.
The fences-versus-guards interval includes zero.

Cost uses known tokens across each arm divided by its solves, including
unsuccessful trials. The report records 16,204,990 known tokens and **16 rows
with unknown final usage**. The table reports token-per-solve lower bounds,
not complete treatment costs. **Dollar cost is unknown** without provider
pricing evidence; no dollar savings are claimed.

The failure taxonomy covers 96 unresolved scored outcomes: **16 timeout,
1 command-not-found, 79 other**. Infrastructure voids are separate. Effect-cap
denial is an overlapping diagnostic, not an additional failure category:
it occurred in 53 guards trials and 47 fences trials, including some solves.
Both treatments routed commands through fixed `read,write,exec` caps;
nested shell invocations frequently hit unknown-command/effect denial.
This exposes a **configuration acceptance and capability-integration problem**.
It does not isolate the merits of guards or fences, establish general language
efficacy, or prove safety. The result supports correcting and separately testing
the integration, not claiming agent-outcome improvements from this run.

## Read the evidence, not the slogan

- [Measured product claims and explicit non-claims](claims.md)
- [Shell-interface experiment](https://arxiv.org/html/2609.11999)
- [Programmatic-tool-calling comparison](https://arxiv.org/html/2608.06370v1)
- [Command-boundary study](https://arxiv.org/abs/2608.13547)
