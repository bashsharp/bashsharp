# Bash# for agents: keep the action language familiar, make the boundary explicit

## Status at a glance

| area | status | evidence |
|---|---|---|
| Bash compatibility | **Shipped and measured.** With the dialect off, Bash#'s front door passed all 86 runnable GNU Bash 5.3 fixtures on Linux, macOS and Windows. This is a fixture result, not a claim of full Bash compatibility. | [claims.md](claims.md) |
| Polyglot islands | **Shipped and measured.** Python, TypeScript, Rust, C, C++ and Go islands can use provisioned toolchains instead of a host toolchain. | [claims.md](claims.md) |
| `agentic` | **Shipped and deliberately bounded.** It marks an action boundary plus an exit status; the interpreter does not call a model. | [claims.md](claims.md) |
| Guards, effect decorators and richer agent controls | **Planned.** They are design work, not a shipped safety guarantee. | [claims.md](claims.md) |
| S5 shell-superset experiment | **Pending.** The planned paired experiment compares plain Bash, Bash# with guards, and Bash# with fences. Results, confidence intervals, costs and failure-taxonomy numbers will be added only from its reviewed evidence record. | Experiment plan; no result is claimed |

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
  the same command through one added parser reduced success by 55.4–73.2
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

The planned layer is intentionally narrower than a new agent DSL: declarations
of what an action may do, checks that return an objective result, and clearer
environment feedback. Those ideas follow the research's emphasis on grounded
execution signals, but they remain planned until they are implemented and
measured. In particular, an `agentic` marker is not evidence of model use,
automatic repair, authorization, or sandboxing; those are explicitly not
claimed in [claims.md](claims.md).

## How we will test the hypothesis

S5 is the evidence step, not a marketing result. It will use the same tasks
and model budget across three arms:

1. Plain Bash.
2. Bash# with guards.
3. Bash# with fences.

The result is pending. Until the evidence record is reviewed, this page makes
no claim that any arm improves task success, cost, safety, or reliability.
When it arrives, the update will name the task count, confidence interval,
cost per solve, and failure taxonomy alongside the raw evidence.

## Read the evidence, not the slogan

- [Measured product claims and explicit non-claims](claims.md)
- [Shell-interface experiment](https://arxiv.org/html/2609.11999)
- [Programmatic-tool-calling comparison](https://arxiv.org/html/2608.06370v1)
- [Command-boundary study](https://arxiv.org/abs/2608.13547)
