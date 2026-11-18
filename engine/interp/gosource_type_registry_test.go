//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGoSourceOriginalTypeRegistryThreeModes(t *testing.T) {
	for name, want := range map[string]string{
		"index.go.txt":          "398dd72a4b01efa279d571667bedbcccd1bbbceaeb77218635db48edf38fd983",
		"list.go.txt":           "ab72fc4fd321f507abde796abe0830876e0f8bcd8661d9ce4ac34c3da2f68060",
		"slice-literals.go.txt": "b085416ef12627bbd6034c78314d61df30a66e4b5d2b3b6cbfebd5befa27c897",
	} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata/gosource-type-registry", name))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(source)) != want {
				t.Fatal("original bytes changed")
			}
			typedSendThreeModes(t, string(source))
		})
	}
}
func TestGoSourceTypeRegistryThreeModes(t *testing.T) {
	cases := map[string]string{
		"typed_alias_conversions":  `package main;import "fmt";type Byte=byte;func text()string{fmt.Println("text");return "hé"};func main(){var a []Byte=[]Byte(text());var b []uint8=[]uint8("hi");var c []int32=[]int32("hé");fmt.Printf("%T %v %s %T %v %s %T %v %s\n",a,a,string(a),b,b,string(b),c,c,string(c))}`,
		"alias_identity":           `package main;import "fmt";type Byte = byte;type Bytes = []Byte;func main(){xs:=Bytes("hi");fmt.Printf("%T %v %s\n",xs,xs,string(xs))}`,
		"entry_protocol_collision": `package main;import "fmt";type entry struct{N int};func main(){m:=map[string][]entry{"a":{{1}}};fmt.Printf("%T %v\n",m,m)}`,
		"local_point":              `package main;import "fmt";func main(){type point struct{X,Y int};p:=make([]point,0,3);fmt.Printf("%T %v\n",p,p[:3])}`,
		"anonymous_private_tag":    "package main;import \"fmt\";func main(){x:=struct{ n int `json:\"n\"` }{7};fmt.Printf(\"%T %v\\n\",x,x)}",
		"generic_explicit":         `package main;import "fmt";func First[T any](xs []T)T{return xs[0]};func main(){fmt.Println(First[int]([]int{7}),First[string]([]string{"yes"}))}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// A reused local type name denotes distinct Go types per scope. Each
// function-local declaration is registered under its own helper identity
// and a package-level one keeps the plain name (Sprint 243, story 674,
// bashpp_s243_scoped_local_types.go), so these valid programs run as native
// Go does instead of being refused for a conflated namespace.
func TestGoSourceTypeRegistryScopedLocalTypes(t *testing.T) {
	for name, source := range map[string]string{
		"local_local":   "package main;import \"fmt\";func one(){type point struct{X int};fmt.Println(point{1})};func two(){type point struct{X int};fmt.Println(point{2})};func main(){one();two()}\n",
		"package_local": "package main;import \"fmt\";type point struct{X int};func main(){type point struct{X int};fmt.Println(point{1})}\n",
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

// A reused function-local generic type name also denotes distinct Go types
// per scope. Since Sprint 275 (story 758) such declarations outside generic
// functions register under their own helper identity, so this valid program
// runs as native Go does instead of being refused; it was previously pinned
// as a refusal (TestGoSourceTypeRegistryRejectsAmbiguousGeneric).
func TestGoSourceTypeRegistryScopedLocalGenerics(t *testing.T) {
	source := "package main;import \"fmt\";func one(){type box[T any] struct{V T};fmt.Println(box[int]{1})};func two(){type box[T any] struct{V T};fmt.Println(box[int]{2})};func main(){one();two()}\n"
	differGoSource(t, source, nil, "")
}
