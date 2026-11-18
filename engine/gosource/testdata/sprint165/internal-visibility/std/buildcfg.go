// The 15 package rows: a cmd/compile/internal package importing a top-level
// internal/… package. Admitted for identity cmd/compile/internal/foo (a
// standard identity), refused for example.com/foo (dotted, never standard).
package foo

import "internal/buildcfg"

var GOOS = buildcfg.GOOS
