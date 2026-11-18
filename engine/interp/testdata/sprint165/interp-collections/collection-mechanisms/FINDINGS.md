# Sprint 165 collection mechanisms

Story #97 (`23e622ce643e`) reselected this family from
`gosource/testdata/sprint151/leaf/active-151-leaf1.tsv` and the existing
Sprint165 collection findings instead of pinning one upstream fixture.

## Mechanisms

| mechanism | active roots | outside-corpus driver |
|---|---:|---|
| IIFE-in-index/slice-bound | 2 | `iife_index_bounds.go` |
| blank-target value builtin | 6 | `blank_builtin_targets.go` |
| typed/interface delete key | 2 | `delete_interface_keys.go`, `zero_map_len_delete.go` |
| collection element numeric carrier | 4 | existing `string-to-uint64` Sprint165 driver |
| collection length const expression | 2 | existing Sprint165 const-array-length evaluator |

The new product changes are general:

- `bashPPCollectionIndex` and slice-bound evaluation accept GoSource call
  results as integer operands, covering IIFE index shapes without teaching the
  scalar evaluator a fixture-specific form.
- `_ = <value-builtin>(...)` now evaluates the builtin and discards the single
  result, preserving side effects such as `copy` and still rejecting no-result
  builtins through the ordinary produced-value check.
- Delete coverage is driven through the existing Sprint165 typed-key table,
  including interface dynamic keys and zero-value maps.

Focused verification:

```sh
go test -count=1 -run 'TestSprint165(ComparableMapKeys|CollectionMechanisms)' ./interp
```

Sprint: #165
Story: #97
Story-ID: 23e622ce643e
