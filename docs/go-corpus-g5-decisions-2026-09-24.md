# Go corpus G5 decisions — 2026-09-24

Pinned source: /Users/qiangli/sdk/go1.27.1/test. “Exclude” names only the
failing mode keys recorded in go-corpus-state-2026-09-24.tsv; “open” adds no
key.

| root | disposition | evidence |
|---|---|---|
| testdir:asmhdr.go | exclude interpreted — assembly-companion | buildrundir; asmhdr.dir/main.s includes generated go_asm.h and supplies values executed by main.go. |
| testdir:closure3.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheckandrundir -0 -m -d=inlfuncswithclosures=1; interpreter has no optimizer diagnostics, but no reviewed catalog basis excludes the compiled key. |
| testdir:codegen/switch.go | exclude interpreted — asmcheck; compiled open | asmcheck source annotations match architecture-specific generated opcodes; interpreter emits no assembly, but no reviewed catalog basis excludes the compiled key. |
| testdir:fixedbugs/issue22877.go | open | builddir has p.s, but the admissible assembly-companion family is expressly execute-phase; this root only builds. |
| testdir:fixedbugs/issue37513.go | exclude interpreted — assembly-companion | buildrundir; sigill_amd64.s executes UD2, and main.go checks its process output. |
| testdir:fixedbugs/issue47317.go | open | builddir has a.s, but no admissible family proves a build-only assembly input is inapplicable. |
| testdir:fixedbugs/issue50372.go | open | errorcheck asserts ordinary range-variable diagnostics; neither state failure nor source proves a gc-only diagnostic. |
| testdir:linknameasm.go | exclude interpreted — assembly-companion | buildrundir; a_amd64.s calls the Go callback and executes during main. |
| testdir:live_regabi.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheckwithauto -0 -l -live ...; interpreter has no optimizer diagnostics, but no reviewed catalog basis excludes the compiled key. |
| testdir:nilptr3.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheck -0 -d=nil; interpreter has no optimizer diagnostics, but no reviewed catalog basis excludes the compiled key. |
| testdir:prove.go | exclude interpreted — compiler-diagnostic; compiled open | errorcheck -0 -d=ssa/prove/debug=1; interpreter has no SSA proof output, but no reviewed catalog basis excludes the compiled key. |
| testdir:retjmp.go | exclude interpreted — assembly-companion | buildrundir; retjmp.dir/a.s uses assembly RET targets exercised by main.go. |
