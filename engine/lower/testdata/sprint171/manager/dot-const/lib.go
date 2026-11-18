// Outside-corpus reproducer (go/types resolver.go's shape): a constant with
// no written type whose initializer is a DOT-IMPORTED constant of a named
// type. The interpreter is told the inferred named type; generated Go must
// leave it to gc, because spelling it would qualify the type by a package
// name the file never binds (`const code errors.Code = WrongAssignCount`).
package lib

import (
	"fmt"
	. "time"
)

func Report() string {
	const unit = Millisecond
	const (
		first  = Second
		second = first * 2
	)
	return fmt.Sprint(unit, first, second)
}
