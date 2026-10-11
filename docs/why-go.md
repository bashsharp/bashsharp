# Why Go is the host syntax for Bash#

Bash# (Bash++) extends bash 5.3 with the constructs of one general-purpose
language. This document explains why that language is Go and not C/C++, C#,
Rust, Python, Java or TypeScript.

## Requirements

A language that extends bash must:

1. **Coexist with shell grammar.** Its constructs share a file with ordinary
   shell commands, and each has a start shape the parser can recognise, with
   a fallback to shell on a near miss.
2. **Match bash's semantic model.** Its concepts must map onto processes,
   exit status, pipes and background jobs.
3. **Be simple.** It needs a small specification and one obvious way to do
   things, because scripts are written fast and read by humans and agents.
4. **Lower to native code.** Every construct must compile to ordinary code of
   that language, so interpreted and compiled runs produce identical output,
   and the result must be a fast, static, cross-OS binary.
5. **Offer an off-ramp.** A script that outgrows shell should become a normal
   program in that language without a rewrite.

## Comparison

++ strong · + adequate · − weak · −− disqualifying

| Language | Grammar | Semantics | Simplicity | Lowering | Off-ramp |
|---|---|---|---|---|---|
| **Go** | + | ++ | ++ | ++ | ++ |
| Python | −− significant indentation | − exceptions | ++ | −− no static binary | + |
| TypeScript | −− `$` in identifiers, backtick templates | − exceptions, async/await | − | − needs a runtime | + |
| Rust | − `'a` lifetimes, `!` macros, `::` | + `Result` | −− ownership, borrow checker | + slow compiles | + |
| C/C++ | −− `#include` is a comment, `<>` templates read as redirections, `int x` reads as a command | − | −− | + heavy toolchain | − |
| C# | − `$"…"`, `@`, `[Attr]` collide with `[` | − exceptions | − | − needs the CLR | + |
| Java | − mandatory class wrapper, verbose | − exceptions | − | −− needs the JVM | − |

## Why Go

**Grammar.** Go needs no semicolons, has no significant whitespace, uses
braces like bash and has only 25 keywords. Its declarations (`func`,
`import`, `type`, `var`, `const`) are unambiguous start shapes. Go does have
some characters that shell also uses: backticks, `*`, `&` and `<-`. These are
resolved by position: at command position the shell meaning applies.

**Semantics.** Each core Go concept has a shell counterpart:

| Go | bash |
|---|---|
| goroutine | `cmd &` |
| channel, `select` | pipe, FIFO, coproc |
| `defer` | `trap … RETURN/EXIT` |
| `(T, error)` | exit status, `\|\|` |
| `import` | `source` |

Go has no exceptions, and neither does bash, so error handling stays explicit
in both. Constructs that cannot lower to Go are refused, even when they would
parse: try/catch, async/await, operator and method overloading, inheritance,
ternary, `?.` and `??`. Admitting them would split the language into two
semantics.

**Simplicity.** Go has a short specification, few features and one idiomatic
form for most tasks. A Go developer reads Bash# constructs without learning
shell-specific substitutes.

**Lowering.** Go compiles in seconds to a single static binary for Linux,
macOS and Windows with no runtime to install. This keeps the "interpreted ==
compiled" guarantee practical.

**Off-ramp.** `transpile` turns a Bash# script into ordinary Go. A file that
is mostly shell stays a script; one that grows ends up as a Go program.

## Trade-offs accepted

- Python has a larger audience and library ecosystem.
- Rust offers stronger safety guarantees and more expressiveness.
- TypeScript is more widely known among web developers.
- Go has no ternary operator, and `if err != nil` is verbose.

These languages remain available inside Bash# as fenced blocks
(`~~~python`, `~~~rust`, `~~~typescript`, `~~~c`, `~~~cpp`, `~~~csharp`,
`~~~powershell`).
Each block runs on a toolchain provisioned on demand, so Bash# gets their
libraries without adopting their syntax.

## Conclusion

Go is the only candidate with no disqualifying weakness. It coexists with
shell grammar, its concepts match bash's, its spec is small, it compiles
cheaply to a static binary on every OS, and it gives scripts a natural path
to a full program.

## References

- [bashpp-import-resolution.md](bashpp-import-resolution.md) §3 (superset of bash and Go)
- [bashpp-dag-tiered-orchestration.md](bashpp-dag-tiered-orchestration.md) §2 (Go construct map)
- [bashsharp-ergonomics-tier.md](bashsharp-ergonomics-tier.md) (four admission tests)
- [why-bashsharp.md](why-bashsharp.md) (comparisons, fences, transpile)
- [go-delta.md](go-delta.md) (construct-level differences from Go)
- [powershell-csharp-fences.md](powershell-csharp-fences.md) (C# and PowerShell fences)
