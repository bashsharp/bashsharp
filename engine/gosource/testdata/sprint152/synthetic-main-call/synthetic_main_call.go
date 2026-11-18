// The synthetic `main` wrapper's call to the renamed source main is glue with
// no line in any source file. Borrowing the first declaration's position
// stamps a //line on it and mis-attributes -0 -m diagnostics to that line
// (the C5 converter half in the Spike-F fidelity FINDINGS). The first
// declaration here is the import, so the borrowed position landed on the
// import's line. The program's output is unaffected; the test asserts the
// synthetic call carries no borrowed //line.
package main

import "fmt"

func helper() int { return 7 }

func main() {
	fmt.Println(helper())
}
