# Sprint 151 / Story #74 — pointer, nil, and selector closure

Post-fix census of `../leaf/S151.3_pointers.roots`, using Go 1.27 and a
`bashy.real` rebuilt against this workspace: **16 of 89 roots pass; 73 remain**.
No `gosource` product code was changed. The small programs in this directory
are outside-corpus controls and are compared byte-for-byte with `go run` by
`interp/bashpp_selector_gosource_test.go`.

## Implemented mechanisms

- Function call results are read from their authoritative result cell. Pointer
  results now retain identity through dereference, selector, and assignment.
- An addressable direct receiver of a pointer-receiver method is bound by
  address. Saved method values no longer turn the receiver into a scalar.
- `new` and address expressions can feed structured readers, including Go's
  implicit dereference when slicing a pointer to an array.
- String index and slice expressions stay in the scalar evaluator even when a
  selector, comparison, or assignment asks the structured reader for them.
- Nil functions keep nilable comparison context; bare nil channel results are
  admitted until result-type coercion supplies their channel type.
- Collection-producing conversions and structured one-result type assertions
  can feed index, dereference, and selector expressions.
- Structured writes follow Go's implicit selector dereference when an
  instantiated generic receiver leaves a pointer-valued parent in the address
  path; invalid parents now diagnose instead of panicking the interpreter.

The corpus roots now passing are:

`fixedbugs/bug045.go`, `fixedbugs/bug447.go`, `fixedbugs/issue10253.go`,
`fixedbugs/issue12226.go`, `fixedbugs/issue13162.go`,
`fixedbugs/issue48473.go`, `fixedbugs/issue48476.go`,
`fixedbugs/issue78303_1.go`, `fixedbugs/issue78303_2.go`,
`fixedbugs/issue7863.go`, `fixedbugs/issue8036.go`,
`fixedbugs/issue8325.go`, `interface/receiver.go`, `nul1.go`, `slice3.go`, and
`stackobj2.go`.

## Remaining roots

### Recoverable nil dereference is still terminal (23)

These programs deliberately execute a nil dereference and recover it, or test
panic-site ordering. The evaluator emits `BASHPP-ENIL-DEREF` as an ordinary
terminal diagnostic before the Go panic/recover machinery can unwind. This is
no longer an addressability or pointer-value loss: it needs the diagnostic to
enter the represented Go panic path.

`chan/powser1.go`, `fixedbugs/bug347.go`, `fixedbugs/bug348.go`,
`fixedbugs/issue19246.go`, `fixedbugs/issue22881.go`,
`fixedbugs/issue23017.go`, `fixedbugs/issue23837.go`,
`fixedbugs/issue27201.go`, `fixedbugs/issue27518a.go`,
`fixedbugs/issue32288.go`, `fixedbugs/issue33724.go`,
`fixedbugs/issue34123.go`, `fixedbugs/issue38496.go`,
`fixedbugs/issue40629.go`, `fixedbugs/issue43835.go`,
`fixedbugs/issue4562.go`, `fixedbugs/issue72860.go`,
`fixedbugs/issue73476.go`, `fixedbugs/issue73748a.go`,
`fixedbugs/issue73748b.go`, `fixedbugs/issue8048.go`,
`fixedbugs/issue8132.go`, `fixedbugs/issue8336.go`.

### Nil reaches a scalar-only consumer (13)

These still report `BASHPP-EEXPR-NIL`. Their nil operands are evaluated by
predeclared print/defer, switch, variadic, or other scalar-only call paths which
bypass the typed value-cell path fixed here. The aggregate nil review test
already records the same pre-existing family on Darwin.

`deferprint.go`, `fixedbugs/bug444.go`, `fixedbugs/bug450.go`,
`fixedbugs/issue19911.go`, `fixedbugs/issue23814.go`,
`fixedbugs/issue25897a.go`, `fixedbugs/issue39541.go`,
`fixedbugs/issue44830.go`, `fixedbugs/issue52788.go`,
`fixedbugs/issue52788a.go`, `fixedbugs/issue53635.go`, `print.go`, `switch.go`.

### Unsafe/native conversions and scalar follow-on gaps (13)

Seven roots now stop at `BASHPP-EEXPR-FORM`, four at
`BASHPP-EEXPR-OPERAND`, one at `BASHPP-ECOLLECTION-STORAGE`, and one at
`BASHPP-ECOLLECTION-ASSIGN`. They involve unsafe pointer reinterpretation,
slice-to-array-pointer conversion, generic interface conversion, complex
values, or string assignment lowering. These are no longer selector-root
classification failures.

- Expression form: `cmp.go`, `fixedbugs/issue17381.go`,
  `fixedbugs/issue4585.go`, `fixedbugs/issue52612.go`, `ken/cplx3.go`,
  `slicecap.go`, `unsafe_slice_data.go`.
- Operand: `convert4.go`, `fixedbugs/issue5809.go`,
  `typeparam/issue51700.go`, `typeparam/issue52228.go`.
- Collection storage/assignment: `fixedbugs/issue54467.go`, `ken/string.go`.

### Assignment-specific pointer and selector targets (8)

The general read paths work, but select receive targets, tuple LHS staging,
native slices, and linked global assignments use separate target evaluators.

- `BASHPP-EPOINTER-TARGET`: `chan/select5.go`,
  `fixedbugs/issue79874.go`, `fixedbugs/issue80188.go`.
- `BASHPP-ESELECTOR-ASSIGN`: `chan/select2.go`, `finprofiled.go`,
  `fixedbugs/issue8606b.go`, `fixedbugs/issue9110.go`, `linkx_run.go`.

### Remaining selector/method-value representation (10)

These require function-value instantiation, interface method values inside
aggregates, dependency-owned struct headers, or method-set support beyond the
field/call/index/deref reader mechanisms implemented here.

- `BASHPP-ESELECTOR-EXPR`: `fixedbugs/issue13169.go`.
- `BASHPP-ESELECTOR-ROOT`: `fixedbugs/issue51401.go`, `strcopy.go`,
  `typeparam/dictionaryCapture-noinline.go`,
  `typeparam/dictionaryCapture.go`, `typeparam/issue44688.go`.
- `BASHPP-ESELECTOR-TYPE`: `gc2.go`, `init1.go`, `ken/rob1.go`.
- `BASHPP-ESELECTOR-UNKNOWN`: `fixedbugs/bug474.go`.

### Harness or later semantic failures (6)

`fixedbugs/bug242.go` and `fixedbugs/bug262.go` get beyond their original
pointer-result failures and now reach later program assertions, so their
remaining mismatch is evaluation order/map comma-assignment semantics.
`fixedbugs/issue15646.go`, `fixedbugs/issue40252.go`,
`fixedbugs/issue4252.go`, and `uintptrescapes.go` use `rundir`; passing only the
root file to the documented single-file command does not load the `.dir`
companion package and reports that no runnable `main` exists. They need a
multi-file/root-directory harness invocation before runtime attribution.
