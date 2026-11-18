// A cmd/compile identity importing a cmd/internal/… package: admitted by the
// parent-of-internal branch (cmd), refused for a dotted identity.
package foo

import "cmd/internal/src"

var _ src.Pos
