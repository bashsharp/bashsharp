// A locally declared array type whose length is a constant name. The helper
// never sees the original constant declarations, so the type is refused from
// materialisation rather than emitted as an uncompilable declaration.
package main

import "fmt"

const width = 3

type row [width]int

func main() {
	var r row
	r[1] = 7
	fmt.Println("cells", r[1], len(r))
}
