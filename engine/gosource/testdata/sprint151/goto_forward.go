// Mechanism: forward goto. The branch is visited before its LabeledStmt, so
// the converter reports the labeled branch first.
package main

import "fmt"

func main() {
	x := 1
	if x > 0 {
		goto done
	}
	x = 99
done:
	fmt.Println(x)
}
