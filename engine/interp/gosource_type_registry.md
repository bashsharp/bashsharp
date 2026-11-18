# GoSource declaration registration

Sprint: #118; Story: #53; Story-ID: 99bd1de0093b

## The gap

Go resolves package-level identifiers over the whole package, not over the text
preceding them. The interpreter executes a lowered gosource tree as a sequence
of statements and registered each type declaration as it reached it, so a
declaration could only see the ones written above it.

The Tour's crawler exercise is ordinary Go that this rejects:

```go
type fakeFetcher map[string]*fakeResult

type fakeResult struct {
	body string
	urls []string
}
```

`exercise-web-crawler.go:41:1: undefined type: fakeResult`.

Reordering the declarations is not a fix. Mutually recursive types
(`type a struct{ b *b }` / `type b struct{ a *a }`) have no order in which each
is written after the other.

## The shape of the fix

Registration is split in two passes over the same statements.

1. `bashPPGoSourceRegisterTypes` (`gosource_type_registry.go`) runs once, before
   the first statement, and installs every package-level type declaration in
   `r.bashPPTypes` under its name.
2. The ordinary statement walk then validates each declaration's representation
   and binds it where it stands, exactly as before.

`bashPPDeclare` distinguishes an entry pass one put there from a real clash by
claiming it: `bashPPGoSourceClaimType` consumes the claim, so a genuine
redeclaration is still diagnosed.

Only the registry is populated early. Variable initializers still run in package
initialization order, so `initializer_order` in the tests observes Go's order.

### What is deliberately not hoisted

- Declarations inside function bodies. They keep sequential, block-scoped
  treatment, so Go's scoping and shadowing rules are unchanged, and a reused
  local spelling is still refused rather than conflated.
- A name this file declares twice, or one already bound by an earlier `Run` on
  the same runner. Pass one leaves the registry alone so pass two reports the
  redeclaration at the line that wrote it.
- Enum members. A forward reference can name the type, never one of its members.
- Classic Bash++. The pass is gated on `File.GoSource`; a `.bpp` script has no
  package scope and still requires declaration before use.

The recursion checks in `bashPPValidateTypeRepresentation` were already correct
once both names resolve: a pointer, slice, or map hop resets the direct set, so
`a`/`b` through pointers is accepted and the same pair by value is still
`cyclic type declaration`.

## Empty interface constraints

gosource lowers `any` to a literal empty interface. `bashPPConstraintSatisfied`
routed that through the implements check, which rejects an uninstantiated type
parameter — the form a generic type takes inside its own declaration:

```go
type List[T any] struct {
	next *List[T]
	val  T
}
```

`BASHPP-EGENERIC-CONSTRAINT: T does not satisfy constraint for T in List`, for a
declaration that was never instantiated. `any` is satisfied by every type, so
the check now answers `true` before asking what the argument is. Named
constraints are untouched and still enforced.

## Corpus status

`interp/testdata/gosource-type-registry` holds the SHA-256-bound Tour originals.
`exercise-web-crawler.go` and `generics/list.go` now pass all three modes
(native oracle, interpreter, transpile). Two same-cluster originals still fail,
for reasons that are not declaration registration, and
`TestGoSourceOriginalForwardTypeRemainingGaps` pins their diagnostics:

| original | diagnostic | cause |
| --- | --- | --- |
| `solutions/webcrawler.go` | `type struct has no method Lock` | embeds `sync.Mutex` in an anonymous struct; the embedded imported method set is missing |
| `methods/errors.go` | `unregistered bridge type "*MyError"` | `MyError` has a `time.Time` field, which the dependency helper cannot materialise, so no local codec is emitted |

Neither is reachable from the registry: the first needs embedded native method
sets, the second needs the local-type codec generator to render a struct with an
imported field type.
