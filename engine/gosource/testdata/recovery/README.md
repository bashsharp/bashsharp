# Original Go parser-recovery diagnostic fixtures

Sprint: #118; Story: #53; Story-ID: 99bd1de0093b

These are complete, unchanged files from the authenticated Go 1.27.0 SDK at
`~/.bashy/sprint118/sources/go-full-sdk/root/src/internal/types/testdata/`:

| Fixture | Original path | SHA-256 |
| --- | --- | --- |
| expr3.go.txt | check/expr3.go | 09291a9472f94a3001a74d01f46aa25fbb8a0490973c803c3dbbd13f0087aad9 |
| stmt0.go.txt | check/stmt0.go | 29472d473c7ed9ad26b3d762837e8c1757b85473637a8ab28f12510a8ad249a0 |
| issue43190.go.txt | fixedbugs/issue43190.go | def735fe9882adcc1af85ce2d59c3a97e0a3444cc054575719f9cb9cef32c02a |

`recovery_test.go` authenticates every byte before loading. The expected flow
matches the pinned SDK's `go/types/check_test.go`: `parseFiles` retains nonnil
ASTs after scanner errors, then `testFiles` invokes the semantic checker. The
product returns all parser and semantic diagnostics, preserving their concrete
error types and original positions, and always returns a nil Program on error.
No recovered AST is converted or executed.

The tests compare the exact diagnostic multiset with an independent public
parser/checker flow. They intentionally do not inject upstream-only `assert`
builtins or special fixture importers. Those remaining capabilities are separate
from parser recovery; these tests are not a claim that all original annotated
errors now match the complete upstream test environment.

Observed diagnostic totals with Go 1.27.0: expr3 has 7 parser + 182 semantic
errors; stmt0 has 4 + 195; issue43190 has 8 + 1. The latter now includes its
original line 11 empty-import error. Multi-file recovery, malformed package
clauses, nil Program containment, and valid execution after failed loads are
covered independently.
