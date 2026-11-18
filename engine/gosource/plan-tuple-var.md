# Tuple variable declarations

Sprint #118, Story #53, Story-ID `99bd1de0093b`.

A checked `var a, b = expression` previously failed conversion. The frontend
now emits one existing structured short declaration into collision-free
internal temporaries, followed by the real typed declarations. This retains
single evaluation and keeps local names outside their initializer's scope.
Package tuples are emitted together at their position in `types.Info.InitOrder`.
The original source bytes are unchanged.

Map lookup, receive, type assertion, and multi-result calls use the existing
interpreter operations. Computed assertion operands are evaluated into an
interface temporary before assertion. Discarded results are explicitly
consumed without declaring `_`. The comma-ok boolean retains its checked type,
including explicit named boolean types and interface boxing. Inferred `any`
is represented by its interface shape when synthesizing a type AST.

Focused controls compare native, interpreted, and compiled-artifact behavior
for single evaluation, absent map/assertion results, closed channels, lexical
shadowing, discarded results, named booleans, pointer identity, package order,
and generated-name collisions. Original Go 1.27 fixtures retain their Go
Authors headers, license, and hashes in `interp/testdata/gosource-tuple-var`.
Invalid arity, redeclarations, initializer scope, and comma-ok types remain
errors before conversion. The original generic issue66878 fixture is checked
and lowered without executing its test-only function bodies.

This change does not certify full corpus execution or implement remaining
runtime/recipe gaps. In particular, an authored preliminary probe found that
a channel created by a package `var c = make(chan int, 2)` was not usable as an
interpreted channel; the committed receive control creates its channel locally
and tests the tuple operation independently. That preexisting declaration gap
requires its own runtime correction.
