# Contributing to Bash#

Bash# is alpha and shaped by the people using it. Three kinds of contribution,
three doors:

1. **A bug** — a Bash 5.3 program that means something different, a POSIX
   case, a Go-corpus root: file it (or fix it) in
   [bashsharp-tests](https://github.com/bashsharp/bashsharp-tests/blob/main/CONTRIBUTING.md),
   where every claim is measured. The `corpus-repair` `good first issue`s are
   bounded and come with their first cause.
2. **A syntax change** — anything a program cannot write today: a two-page
   RFC under [`rfcs/`](rfcs/) from the template. It must name the shape, its
   collision class against stock bash (rejected → purely additive; accepted →
   needs a commit signal and an escape), how it lowers to plain Go, and one
   fixture. Nothing is admitted that cannot lower to ordinary Go or that
   re-spells something Go already has. During alpha the maintainer is the
   sole approver; expect a written reason either way.
3. **A fenced language** — one island plus one real-repo example, from the
   "island of the month" issue template on bashsharp-tests.

The code in this repo is the language front door (`front/`, `transpile/`,
`cmd/bashsharp`); the evaluator and lowering live in
[qiangli/sh](https://github.com/qiangli/sh) (`interp/`, `lower/`), and a
change there is measured here and in bashsharp-tests before it is anything.
Read [ROADMAP.md](ROADMAP.md) for what is stable, alpha, and open.

Rules: every number names its corpus ([docs/claims.md](docs/claims.md));
no private details in public repos; DCO sign-off on commits (`git commit -s`).

Questions go to [Discussions](https://github.com/bashsharp/bashsharp/discussions);
the pinned FAQ has the common ones.
