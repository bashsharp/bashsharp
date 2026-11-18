# Original Go joint multi-file import-declaration fixtures

Sprint: #118; Story: #53; Story-ID: 99bd1de0093b

These are complete, unchanged files from the authenticated Go 1.27.0 SDK at
`~/.bashy/sprint118/sources/go-full-sdk/root/src/internal/types/testdata/check/importdecl0/`:

| Fixture | Original path | SHA-256 |
| --- | --- | --- |
| importdecl0a.go.txt | check/importdecl0/importdecl0a.go | 917ee6d97fa48e038f5fc2db6dbc719a42f2bcdd13c412a783a52d660a87375d |
| importdecl0b.go.txt | check/importdecl0/importdecl0b.go | 2577e408851b08cc0ed04e521fa02d9f6fe5945160f416c827b38ad954e6395c |

Both files form a single package and must be checked jointly: `importdecl0b.go`
dot-imports `testing` and `fmt`, declaring `T` and `Println` in file scope, and
`importdecl0a.go` dot-imports `reflect`. Unlike the parser-recovery fixtures in
`../recovery`, these files parse cleanly; every annotated `/* ERROR ... */` is a
semantic (type-checker) diagnostic, including three invalid-import-path errors:

```go
import (
	"" /* ERROR "invalid import path" */
	"a!b" /* ERROR "invalid import path" */
	"abc\xffdef" /* ERROR "invalid import path" */
)
```

An invalid import path is reported by the checker's `Error` callback, not the
importer, so the frontend must keep collecting every positioned diagnostic
across both files rather than aborting on the first one. `importdecl_test.go`
authenticates every byte, then compares the product's diagnostic multiset with
an independent public parser/checker flow using the same importer, and confirms
each embedded `ERROR`/`ERRORx` annotation is satisfied at its source position
after dropping the checker's secondary `: \t` continuation lines — the same
elimination the pinned SDK's `go/types/check_test.go` performs.

These tests do not inject upstream-only `assert` builtins or special fixture
importers, and are not a claim that the external check-harness recipe
("joint-multi-file-package-check") is complete; they pin the frontend behavior
these two `TestCheck/importdecl0` roots depend on.
