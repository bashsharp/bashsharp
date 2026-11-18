//go:build full

package interp_test

import "testing"

// A short declaration can rebind a named result alongside new locals. The
// result remains live until the bare return, including after the new locals
// leave their last-use positions.
func TestS270G6NamedResultShortRedeclare(t *testing.T) {
	source := `package main
func values() (float32, int, string) { return 1, 2, "three" }
func result() (s string) {
	a, b, s := values()
	_, _ = a, b
	return
}
func main() { println(result()) }
`
	differGoSource(t, source, nil, "")
}
