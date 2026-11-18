// testdir:escape_runtime_atomic.go's shape: the testdir harness compiles a
// single errorcheck file with -p p, and p is a standard identity.
package escape

import (
	"internal/runtime/atomic"
	"unsafe"
)

func Loadp(addr unsafe.Pointer) unsafe.Pointer { return atomic.Loadp(addr) }
