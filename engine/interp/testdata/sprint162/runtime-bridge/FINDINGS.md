# Sprint 162 runtime-bridge findings

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:typeparam/issue47272.go` | A generic local value reached a mirrored method with the helper's generated instantiated type name; the callback receiver was rebuilt as the uninstantiated base type. | Recover the original instantiated wire type from the generated helper descriptor before rebuilding the callback receiver. | fixed in the runtime-bridge commit; leaf confirmation pending |
| `testdir:typeparam/issue48317.go` | Reduced JSON round-trip uses the generic local type materialisation path, not callback lifecycle. | Generic local type descriptor and pointer writeback; no new runtime-bridge defect reproduced. | design/adjacent registration path; confirm with the leaf before movement |
| `testdir:typeparam/issue48598.go` | A typed-nil generic function type is handed through an interface and needs nil/interface registration. | Generic nil interface transport, not callback receiver reconstruction. | moved to the type/collection seam |
| `testdir:typeparam/issue50481c.go` | Generic local type alias/value registration is reached before any callback is retained. | Generic descriptor closure and alias materialisation. | moved to the type/collection seam |
| `testdir:typeparam/nested.go` | Nested generic local type identity/reflection is a compiler/runtime type identity case. | No bridge callback lifecycle involved in the reduced path. | moved to the type/collection seam |
| `testdir:fixedbugs/gcc65755.go` | Two function-local types share the spelling `s`; the bridge cannot conflate their helper identities. | Local type namespace collision, not generic callback lifecycle. | moved to the type/collection seam |
| `testdir:fixedbugs/bug257.go` | Large constant/string execution does not exercise the bridge callback seam. | Native dependency/value path requires separate reproduction. | not reproduced; moved to the evaluator/literal seam |

## Requests to other seams

| `testdir:const7.go`, `fixedbugs/issue11771.go`, `fixedbugs/issue54542.go`, `nosplit.go`, `fixedbugs/issue19658.go`, `linkx_run.go`, `fixedbugs/issue9110.go`, `chan/select2.go`, `finprofiled.go`, `fixedbugs/issue8606b.go` | Dependency-owned native handle fields and exported package variables were not addressable through the bridge. | General `field-set` and `var-set` protocol operations with Go assignability enforced by reflection. | fixed in `9bfb16c1`, `470e85ab`; leaf confirmation pending |
| `testdir:recover-error` (outside-corpus reproducer) | A recovered dependency error was rendered from the interpreter's diagnostic representation (`errors.New("original error")`) rather than by the dependency's error interface. | Native error payload rendering through the recover interface still needs a general dynamic-value bridge path. | design; no safe fix in this pass |

The generic nil/interface, alias/descriptor-closure, nested identity, and
same-spelling local-type rows need the type/collection owner to preserve their
distinct original identities. No file change is requested here.
