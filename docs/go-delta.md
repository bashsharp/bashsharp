# Go delta: where Bash# differs from Go

<!-- GENERATED from godelta/go-delta.tsv by `go generate ./godelta`. Do not edit. -->

One row per Go construct whose behaviour differs from Go, keyed by Go spec section and grammar production. The table is published instead of a percentage: it says which constructs, and what to do instead. Every row is proven by a fixture in `bashsharp-tests/tests/go-delta/` run by `tools/go-delta-gate.sh`. The same rows are available from the command line as `bashy explain go` (`--json`, `--tsv`, `--md`); `bashy explain go "<refusal text>"` resolves a refusal to its row and workaround.

Go reaches Bash# on three surfaces, and the rows say which one they are about:

- **mixed**: Go among shell statements in a `.bsh` script (`bashy --bashsharp FILE`). Bash# interprets it.
- **unit**: a file that begins with a `package` clause. Bash# interprets the whole unit; this is the Tour of Go and Go by Example path.
- **island**: a `~~~go` fence, `embed go "./file.go"`, a `.go` file or `--source=go`. The provisioned Go toolchain compiles it, so it is Go.

Status: `supported` matches Go and is proven; `limited` works with a stated difference; `gap` is not shipped (a defect or a missing feature, with a workaround); `excluded` is refused by design.

26 rows: 12 supported, 3 limited, 4 gap, 7 excluded.

## Mixed

| ID | Spec section | Production | Status | Reason | Workaround | Diagnostic | Fixture |
|---|---|---|---|---|---|---|---|
| M01 | Short variable declarations | ShortVarDecl | supported | Go binding at statement start; shell expansion sees the value's text |  |  | `M01.bsh` |
| M02 | Function declarations | FunctionDecl | supported | Typed function; a call written as a shell word binds or expands its result |  |  | `M02.bsh` |
| M03 | Function declarations | Result (T, error) | supported | Multiple results bind with :=; a nil error projects as the text nil |  |  | `M03.bsh` |
| M04 | Import declarations | ImportDecl (standard library) | supported | Quoted import path; package calls work as Go expressions |  |  | `M04.bsh` |
| M05 | Composite literals | CompositeLit (map), index assignment, len | supported | Map literal, element assignment and len behave as in Go |  |  | `M05.bsh` |
| M06 | Send statements | SendStmt, receive operator, GoStmt | supported | Channels and a go statement with a function literal |  |  | `M06.bsh` |
| M07 | Address operators | UnaryExpr & and * (assignment through a pointer) | supported | Pointer to a Go binding; assignment through it updates the binding |  |  | `M07.bsh` |
| M08 | Go statements | GoStmt closure capture | limited | A goroutine started in mixed text receives a snapshot of ordinary Go bindings, so its writes do not reach the parent (Go would print x=9); channels are the synchronization and transfer mechanism | Send the value back on a channel, or put the shared-variable code in a package-led Go unit or a Go island |  | `M08.bsh` |
| M09 | If statements | IfStmt between shell commands | gap | Top-level Go control-flow blocks are not shipped; they work inside a typed func body | Move the block into a func and call it (bashsharp-tour 03-go/02-funcs), or run the code as a package-led Go unit | `` `}` can only be used to close a block `` | `M09.bsh` |
| M10 | For statements | RangeClause in a func body | gap | A range loop in a mixed-text func body is not accepted; counted for loops are | Use an indexed for loop, or a package-led Go unit or Go island | `for condition must be a scalar expression` | `M10.bsh` |
| M11 | Type parameter declarations | TypeConstraint with a union (int \| float64) | gap | Union constraints are rejected at the declaration | Constrain with any or comparable, or use a package-led Go unit or Go island | `` syntax error near unexpected token `float64]' `` | `M11.bsh` |
| M12 | Selectors | SelectorExpr on a struct value bound at the top level | gap | A typed function that returns a field of a struct parameter yields the selector text (j.ID) instead of the value. A defect, not a design choice | Use a package-led Go unit (U01) or a Go island, where struct values and methods work |  | `M12.bsh` |
| M13 | Type declarations | TypeDecl (type NAME TYPE) | limited | type NAME TYPE is always a Go declaration in the dialect, so the shell builtin form with two operands now reports an undefined type | command type foo bar (explicit builtin) | `undefined type: bar` | `M13.bsh` |
| M14 | Comments | Unquoted // at the start of a word | limited | // begins a comment in mixed text even after a shell command; http://x and quoted words stay literal | Quote the word: "//path" |  | `M14.bsh` |
| M15 | Source file | PackageClause after the first statement | excluded | A package clause starts a whole Go compilation unit; it is not a declaration inside a script | Start the file with package (a Go unit), or put the program in a .go file, a ~~~go fence or embed go | `package must begin a Go compilation unit` | `M15.bsh` |
| M16 | Import declarations | ImportSpec with a raw-string path | excluded | Backquotes are shell command substitution at a mixed start site | Use a double-quoted import path | `invalid import statement; use command import` | `M16.bsh` |
| M17 | Import declarations | import "C" (cgo) | excluded | cgo needs a C toolchain and native ABI outside the mixed evaluator | Put the C source in a ~~~c island, or use a Go unit with a cgo preamble | `no authenticated package-scoped cgo metadata` | `M17.bsh` |

## Unit

| ID | Spec section | Production | Status | Reason | Workaround | Diagnostic | Fixture |
|---|---|---|---|---|---|---|---|
| U01 | Source file | SourceFile led by a package clause (.bsh or stdin) | supported | A file that starts with package runs as an interpreted Go unit: types, methods, interfaces, generics, closures, goroutines. Tour of Go 97/97 and Go by Example 85/85 are measured here |  |  | `U01.bsh` |
| U02 | Package unsafe | Pointer reinterpretation of interpreter-owned values | excluded | Interpreter-owned values have no native byte layout (operator decision 2026-10-04); existing tested emulation stays, none is added | Do the reinterpretation in a ~~~go fence or embed go | `BASHPP-EUNSAFE-VIEW` | `U02.bsh` |

## Island

| ID | Spec section | Production | Status | Reason | Workaround | Diagnostic | Fixture |
|---|---|---|---|---|---|---|---|
| I01 | Function declarations | ~~~go as ALIAS with exported functions | supported | A compiled worker exposes exported functions through a typed bridge; the nearest go.mod supplies the module |  |  | `I01.bsh` |
| I02 | Source file | ~~~go fence holding package main | supported | A package main fence is built with the provisioned toolchain and run as a program with the script's streams and arguments |  |  | `I02.bsh` |
| I03 | Source file | embed go "./file.go" holding package main | supported | The embedded file is built and run like a package main fence |  |  | `I03.bsh` |
| I04 | Source file | package main fence or embed with an alias | excluded | A program is run, not called; it has no functions to expose under an alias | Drop the alias, or expose functions from a non-main package | `package main Go fence runs as a program and cannot have alias` | `I04.bsh` |
| I05 | Source file | A .go file or --source=go FILE | supported | Whole Go source is compiled with the provisioned toolchain from its nearest go.mod; it is not interpreted. An explicit --bashsharp keeps the interpreted route used by the corpus harness |  |  | `I05.go` |
| I06 | Source file | A .go file whose package is not main | excluded | A library package cannot run as a program | Expose its exported functions from a ~~~go fence in a .bsh script | `cannot run as a program` | `I06.go` |
| I07 | Modules | Go fence or embed without a go.mod | excluded | Module context is never discovered from the network or guessed; the nearest go.mod (or a ~~~gomod fence) is required | Add a go.mod beside the script or a ~~~gomod fence | `requires a nearest go.mod` | `I07.bsh` |
