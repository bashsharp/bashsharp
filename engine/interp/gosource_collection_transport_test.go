//go:build full

package interp_test

// Sprint: #118; Story: #3; Story-ID: fa07603b71dc
//
// One coherent collection/byte transport slice for the concrete (non-generic)
// dependency emitters and consumers that read or mutate interpreter-owned slice
// storage: base64/hex encoders, json/xml marshalers, regexp byte matchers and
// the in-place sort.Ints/Strings/Float64s sorters, plus inferred-length array
// literals crossing the reflect-based type resolver.
//
// Each fixture is an unchanged original Go program, authenticated by SHA-256
// before any mode runs, and executed in all three modes — native, interpreted
// and source-free-compiled — whose stdout and stderr must agree byte for byte.
// The byte fixture deliberately carries invalid UTF-8 so agreement proves
// lossless byte transport rather than a lucky printable rendering.
//
// An uninstantiated generic function cannot be reflected as an imported
// dependency symbol, so a generic helper is never dispatched to the dependency.
// slices.Equal and slices.Sort are answered interpreter-side over the values
// that already crossed this transport — Equal on the read-only path, Sort on the
// mutation writeback — and are covered by TestGoSourceGenericSliceHelpers and
// TestGoSourceGenericSliceSortAlias below. Other generic helpers such as
// slices.Contains remain out of scope.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func collectionTransportFixture(t *testing.T, name, digest string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/gosource-collection-transport/" + name + ".go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != digest {
		t.Fatalf("original fixture %s digest changed: %s", name, got)
	}
	return string(raw)
}

// TestGoSourceCollectionTransportThreeModes runs each unchanged original in all
// three modes and requires identical output.
func TestGoSourceCollectionTransportThreeModes(t *testing.T) {
	for name, digest := range map[string]string{
		"byte-transport": "2566bf44ed4effc4cfe1ff3289aeb2c23973c7bc3debb6daf982df3c3e4a80c2",
		"marshal":        "c3204303cc507055120a1f3765fabf2ebbb8f932746138fd619f3800d5f74fa6",
		"sort-mutation":  "7ce6543ddf9f12ee35c2770251467f9e381d3a6cd78c857bf95b291bf3d8477e",
		"inferred-array": "d0b4cfd36e8ccf1c7565ac493b50e851fa858e122eae019f863757a201d8637d",
	} {
		t.Run(name, func(t *testing.T) {
			callbackTourThreeModes(t, collectionTransportFixture(t, name, digest))
		})
	}
}

// TestGoSourceCollectionTransportMutationAlias exercises the in-place mutation
// writeback against aliasing focused cases: every aliasing header must observe
// the reordering, and only the visible length is touched. Element-address
// identity across aliased headers is a separate interpreter concern and is not
// asserted here — aliasing is observed by value, as native Go guarantees.
func TestGoSourceCollectionTransportMutationAlias(t *testing.T) {
	for name, source := range map[string]string{
		// Two headers over one backing array both see the sort.
		"alias_both_sorted": `package main
import("fmt";"sort")
func main(){s:=[]int{4,2,5,1,3};a:=s;sort.Ints(s);fmt.Println(s,a)}`,
		// Sorting a sub-slice leaves the tail of the shared backing untouched.
		"subslice_tail_preserved": `package main
import("fmt";"sort")
func main(){b:=[]int{9,8,7,6,5};h:=b[:3];sort.Ints(h);fmt.Println(h,b)}`,
		// A slice built by append (cap may exceed len) still round-trips exactly.
		"appended_len_vs_cap": `package main
import("fmt";"sort")
func main(){s:=make([]int,0,8);s=append(s,3,1,2);sort.Ints(s);fmt.Println(s,len(s),cap(s))}`,
		// A slice field inside a struct is reordered in place through the field.
		"struct_field_slice": `package main
import("fmt";"sort")
type box struct{ xs []int }
func main(){b:=box{[]int{5,4,3,2,1}};sort.Ints(b.xs);fmt.Println(b.xs)}`,
	} {
		t.Run(name, func(t *testing.T) { callbackTourThreeModes(t, source) })
	}
}

// TestGoSourceGenericSliceHelpers exercises the two generic slices helpers that
// cannot be reflected as dependency symbols and are therefore answered
// interpreter-side: slices.Equal over the read-only transport and slices.Sort
// over the mutation writeback. Each unchanged original is run interpreted and
// against a real Go build; their observable behaviour must agree.
func TestGoSourceGenericSliceHelpers(t *testing.T) {
	for name, source := range map[string]string{
		// slices.Equal reads both transported slices and answers a bool.
		"equal_true": `package main
import("fmt";"slices")
func main(){a:=[]string{"go","sh"};b:=[]string{"go","sh"};fmt.Println(slices.Equal(a,b))}`,
		"equal_false": `package main
import("fmt";"slices")
func main(){a:=[]string{"go","sh"};b:=[]string{"go","zz"};fmt.Println(slices.Equal(a,b))}`,
		"equal_length_mismatch": `package main
import("fmt";"slices")
func main(){a:=[]string{"x"};b:=[]string{"x","y"};fmt.Println(slices.Equal(a,b))}`,
		// A nil slice and an empty slice are equal, as native Go reports.
		"equal_nil_and_empty": `package main
import("fmt";"slices")
func main(){var a []string;b:=[]string{};fmt.Println(slices.Equal(a,b))}`,
		// slices.Sort reorders the visible elements through the backing storage.
		"sort_strings": `package main
import("fmt";"slices")
func main(){s:=[]string{"pear","apple","kiwi","banana"};slices.Sort(s);fmt.Println(s)}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

// TestGoSourceGenericSliceSortAlias mirrors the concrete sort.Ints alias cases
// for the generic slices.Sort: every aliasing header over the shared backing must
// observe the reordering, and a sub-slice sort must leave the tail untouched.
func TestGoSourceGenericSliceSortAlias(t *testing.T) {
	for name, source := range map[string]string{
		// Two headers over one backing array both see the sort.
		"alias_both_sorted": `package main
import("fmt";"slices")
func main(){s:=[]string{"d","b","e","a","c"};a:=s;slices.Sort(s);fmt.Println(s,a)}`,
		// Sorting a sub-slice leaves the tail of the shared backing untouched.
		"subslice_tail_preserved": `package main
import("fmt";"slices")
func main(){b:=[]string{"e","d","c","b","a"};h:=b[:3];slices.Sort(h);fmt.Println(h,b)}`,
		// A slice built by append (cap may exceed len) still round-trips exactly.
		"appended_len_vs_cap": `package main
import("fmt";"slices")
func main(){s:=make([]string,0,8);s=append(s,"c","a","b");slices.Sort(s);fmt.Println(s,len(s),cap(s))}`,
		// A slice field inside a struct is reordered in place through the field.
		"struct_field_slice": `package main
import("fmt";"slices")
type box struct{ xs []string }
func main(){b:=box{[]string{"e","d","c","b","a"}};slices.Sort(b.xs);fmt.Println(b.xs)}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
