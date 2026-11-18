//go:build full

package interp_test

import (
	"testing"
)

// A formatter walking a copied slice synchronizes that backing at every
// callback boundary, so arbitrary value-receiver bodies retain their effects
// and later elements are read from the refreshed copy.
func TestS275FmtValueCallbacksPreserveEffects(t *testing.T) {
	for name, src := range map[string]string{
		"named field method": `package main
import "fmt"
type Tag int
func (Tag) String() string { println("effect"); return "tag" }
type item struct{ n Tag }
func (i item) String() string { return fmt.Sprint("item", i.n) }
func main() { fmt.Println([]item{{1}}); println("after") }`,
		"nested call": `package main
import "fmt"
type item struct{ n int }
func effect() int { println("effect"); return 1 }
func (i item) String() string { return fmt.Sprint("item", effect()) }
func main() { fmt.Println([]item{{1}}); println("after") }`,
	} {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, src, nil, "")
		})
	}
}
