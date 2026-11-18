// Mapped as example.com/other: a foreign module, refused with gc's wording
// (negative). The same file checked under identity example.com/m/z is
// admitted: only the identity differs.
package other

import "example.com/m/internal/x"

var V = x.N
