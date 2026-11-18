These byte-identical Go Tour examples are from golang.org/x/website,
commit c4a9d59f9775d994f1700d18fa37414c3c85fa7b, directory
_content/tour/moretypes. The upstream BSD license is retained in LICENSE.
The .txt suffix keeps original program bytes out of this package's build.

Existing examples are bound by sha256.json. The additional mutating-maps.go
fixture is bound by TestGoSourceOriginalCollectionRepairThreeModes:
`a8f33f1af579189d54372c64e2c9648110b968ea5972e58cc261f7ff9f96a15b`.

The test executes the original bytes in Go, the interpreter, and the compiled
artifact; the source is never rewritten to accommodate the interpreter.
