# Native object members in GoSource

Sprint: #118; Story: #52; Story-ID: d564bada90bb.

Native fields, methods and method values now stay on the dependency handle
path. This fixes local-struct lookup errors for `http.Response.Body`, the
rejected `response.Request.URL` / `url.User.Username()` receiver paths, and
local-method lookup errors while capturing `defer listener.Close()` or
`defer file.Close()`. The original program still executes through Runner;
only imported dependency operations execute in the persistent native helper.
Ordinary Bash/Bash++ dispatch is unchanged.

`bashPPPrepareNativeCall` evaluates the receiver expression and typed arguments.
A direct call binds the receiver when invoking the native method; an explicit
method-value expression snapshots a value receiver when that expression runs.
A pointer receiver retains its native identity. Native deferred calls capture
the bound callee and typed operands at the defer statement, then invoke the
captured request during normal unwinding. The unwind hook preserves a callback panic
already in flight for the separate callback implementation.

The `member` operation returns one exported field value or bound method handle.
The `receiver_field` operation retains an addressable field inside the dependency
process for a following method selection. Ordinary field assignments still copy
value types; selecting a pointer method from a field keeps its original storage.
No unexported field is serialized, no raw pointers leave the dependency process,
and no original function body is compiled into the helper. Reflection copies
of dependency value types remain inside that process, preserving Go value-copy
semantics even when a dependency type internally contains private fields.

Bound function values carry host-only `Callable` provenance (`json:"-"`) for
callback policy. Methods derive it from the dependency's actual reflect type
metadata `NativeType`, which recursively qualifies pointer element names by
package path. This avoids confusing a type from an unrelated package named
`sync` with the standard `sync.Once`. Package function values use the original
import path. The worker cannot supply a callback-policy decision in JSON.
Existing display/type-transfer `Type` behavior is unchanged.

Focused original-source Runner/native differentials cover file Write/Close/Name,
native method values, listener interface methods and a native accept deadline,
typed-nil file methods, nested URL fields, an HTTP response/body from an owned
local server, and deferred argument capture. A private module fixture compares
native field mutation, independent value copies, value-method snapshots,
pointer method values and receiver/argument side-effect ordering. All original
source and dependency bytes are checked unchanged.

The blocking Accept cancellation test checks the reported dependency process
actually dies and its helper files disappear. Separate protocol negatives use
real native handles to reject unexported storage access, expired requests and
stale-session reuse; concurrent reads exercise session transport under the race
detector. These negatives are not presented as original-program coverage.

Validation commands:

```sh
GOMAXPROCS=2 GOFLAGS=-race go test -race -p 2 ./interp -run '^(TestGoSourceNativeMember|TestNativeMemberSession)' -count=1
GOMAXPROCS=2 go test -p 2 ./interp -run '^TestGoSource(NativeMembers|PersistentNativeBridge|ImportedTypeIdentity|NativeStructuredValues|NativeSessionLifecycle|BridgeCancellation|BridgeSessionIsolation)$' -count=1
GOMAXPROCS=2 go test -p 2 ./interp -run '^TestBashPP(StdlibImportSelectorCalls|LocalMultipartSelectorWinsOverImport|FuncRuntimeProbe|GenericFuncRuntime|PromotedPointerFieldAndMethods|PromotedMethodSets|PromotedPointerMethodCallsAndExpressions)$' -count=1
```

The required native Bash confirmation run was also attempted with Homebrew
Bash 5.3. It fails existing expected-output fixtures, including `continue 1 2 3`
and `printf '%(%q)T'`; that test invokes external Bash alone. The exact log is
`/tmp/s118-native-members-bash-confirm.log`. Focused classic Bash++ method/import
regressions pass.

This does not implement arbitrary local callable expressions returning native
receivers, mutation of native fields by assignment, generic imported functions,
or unrestricted callbacks/goroutine sharing. Those remain separately owned.
Frozen candidate006 and its complete failing corpus ledger are unchanged;
new integrated candidate evidence is required for corpus acceptance.
