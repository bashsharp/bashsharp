// Mapped as example.com/m/y: inside the parent of internal, so cmd/go's
// module branch admits the import (positive control).
package y

import "example.com/m/internal/x"

var V = x.N
