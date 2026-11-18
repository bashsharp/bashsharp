Sprint: #118; Story: #54; Story-ID: c3a60493cde9

`image.go.txt` is the unchanged Go Tour solution for the image exercise
(`_content/tour/solutions/image.go` in golang.org/x/website), including its
OMIT directive and original copyright notice. Note the corpus filename is
`solutions/image.go`, not `solutions/images.go`; `methods/images.go` is a
different, callback-free file that only prints a native `image.NewRGBA`.
The fixture is authenticated by SHA-256 before any mode runs. Its dependency,
golang.org/x/tour v0.1.0, is pinned by module checksum.

The original declares three methods on `Image` — `ColorModel() color.Model`,
`Bounds() image.Rectangle` and `At(x, y int) color.Color` — and hands the value
to `pic.ShowImage`, so `image/png` drives all three from dependency code and
then calls `RGBA()` on the `color.RGBA` the original `At` returns.

All three modes pass. Native, interpreted and source-free-compiled are checked
against each other on the decoded original PNG, not merely on the output
string: identical bounds and identical colour at every one of the 65536 pixels,
plus a direct check that `Bounds` is `image.Rect(0, 0, 256, 256)` and that `At`
is `color.RGBA{c, c, 255, 255}` for `c = uint8(x ^ y)`. The compiled artifact
runs after both the original and generated sources are deleted and with no
tools on PATH.

Every original method body stays interpreted. The helper carries only a
generated transport stub per mirrored method, emitted by `bashPPLocalMethodGo`
from the reviewed signature: typed parameters are encoded onto the callback
request, `bashPPNativeCallback` binds them to the original parameters at their
declared types, and each result crosses back as a typed value rather than text.
`color.RGBAModel` and `image.Rect(...)` therefore stay authenticated native
handles, and `color.RGBA{...}` crosses as a structural value decoded to
`color.Color`. No original statement is compiled, no source is rewritten, and
no cap or timeout was weakened.

The method set is admitted whole or not at all — `mirroredImage` matches all
three signatures exactly, so a value serving only part of `image.Image` never
presents itself to the dependency as one. `pic.ShowImage` and `image/png.Encode`
are the reviewed synchronous consumers, matching the existing `wc.Test`,
`pic.Show` and `reader.Validate` policy entries; retained or asynchronous image
consumers stay refused by the general transport policy.

Two supporting boundaries moved with it, both general rather than image-shaped:
a single-expression `return` of an imported value keeps its native handle
instead of being flattened to text, and imported struct literals accept Go's
positional form, filled in the dependency's own field order. Mixing keyed and
positional fields stays refused, as it is in Go.

Review integration preserves the Reader snapshot fast path and includes its
flag in the native descriptor identity. Callback arity and native positional
struct field count/mixing are checked before accepting transport values.
Nested original callback panics during an imported return expression preserve
the active panic/exit state, so an outer original defer can recover without a
spurious scalar-interruption diagnostic or exit status.

`TestGoSourceCallbackBodyFailure` verifies the current positioned collection
bounds diagnostic, a nonnil Runner error, and no continuation after the failed
callback. The older assertion expected a recovered host panic message; collection
access now fails explicitly instead. Full Go bounds-panic/recover compatibility
remains open. A separate valid original method whose nested `sort.Ints` operation
is unsupported verifies the same failure propagation independently.

Full sprint convergence is not claimed. The retained image consumer is rejected
before its code or any original image callback runs; original image methods are
never compiled into the native dependency helper.
