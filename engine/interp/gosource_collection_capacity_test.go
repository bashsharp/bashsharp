//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
//
// Capacity of interpreter-owned collections: the capacity a `[]byte(s)` or
// `[]rune(s)` conversion reserves, and the len/cap of a collection that a
// function call returned. Every case runs the unchanged original Go
// source in the Go SDK, in the interpreter and as a lowered compiled artifact,
// and compares the three. No case asserts an interpreter-only expectation.
//
// The residual capacity differences this file does not claim — the ones the
// compiler's escape analysis produces by putting a backing store on the stack —
// stay measured in the tagged collection gate; see
// docs/plan-gosource-collection-capacity.md.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestGoSourceCollectionConversionCapacity(t *testing.T) {
	cases := map[string]string{
		// These dynamic conversions escape through fmt, selecting the native
		// heap allocation path rather than compiler-reserved stack backing.
		"converted_byte_slice_capacity": `package main

import "fmt"

func text() string  { return "hey" }
func long() string  { return "abcdefghijklmnopqrstuvwxyz0123456789" }
func blank() string { return "" }

func main() {
	short := []byte(text())
	fmt.Println(short, len(short), cap(short))
	wide := []byte(long())
	fmt.Println(wide, len(wide), cap(wide), string(wide))
	empty := []byte(blank())
	fmt.Println(empty, len(empty), cap(empty))
}
`,
		// []rune(s) rounds the same way, but over four-byte elements, so the
		// answer is the size class divided by the element size.
		"converted_rune_slice_capacity": `package main

import "fmt"

func text() string { return "héllo" }
func long() string { return "abcdefghij" }

func main() {
	runes := []rune(text())
	fmt.Println(runes, len(runes), cap(runes))
	fmt.Println(string(runes))
	wide := []rune(long())
	fmt.Println(wide, len(wide), cap(wide), string(wide))
}
`,
		// The surplus the allocator reserved is zeroed and really is part of
		// the same backing array: re-slicing to cap exposes typed zeros, and a
		// later append writes through the shared array rather than copying.
		"converted_spare_capacity_is_shared": `package main

import "fmt"

func text() string { return "hey" }

func main() {
	b := []byte(text())
	full := b[:cap(b)]
	fmt.Println(full, len(full), cap(full))
	b = append(b, 'X')
	fmt.Println(string(b), len(b), cap(b), full[3])
	full[4] = 'Y'
	fmt.Println(b[:cap(b)][4], len(b), cap(b))
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// TestGoSourceCollectionCallResultLenCap covers len and cap over a collection
// the program reached through a call rather than through a name: the operand is
// evaluated once, and its length, capacity and element type survive the result
// boundary.
func TestGoSourceCollectionCallResultLenCap(t *testing.T) {
	cases := map[string]string{
		"call_result_len_cap": `package main

import "fmt"

func made() []int { return make([]int, 3, 9) }

type box struct{ data []string }

func (b box) all() []string  { return b.data }
func (b *box) kept() []string { return b.data }

func main() {
	fmt.Println(len(made()), cap(made()))
	b := box{make([]string, 2, 5)}
	fmt.Println(len(b.all()), cap(b.all()), len(b.kept()), cap(b.kept()))
	fmt.Println(len(made()[1:]), cap(made()[1:]))
}
`,
		"call_result_operand_once": `package main

import "fmt"

func made() []int {
	fmt.Println("made")
	return make([]int, 2, 6)
}

func main() {
	fmt.Println(len(made()))
	fmt.Println(cap(made()))
	fmt.Println(len(append(made(), 1)), cap(append(made(), 1)))
}
`,
		"call_result_map_string_array": `package main

import "fmt"

func table() map[string][]int { return map[string][]int{"a": make([]int, 1, 6)} }
func word() string            { return "hello" }
func fixed() [4]int           { return [4]int{1, 2, 3, 4} }

func main() {
	fmt.Println(len(table()), len(table()["a"]), cap(table()["a"]))
	fmt.Println(len(word()))
	fmt.Println(len(fixed()), cap(fixed()))
}
`,
		"call_result_generic_and_closure": `package main

import "fmt"

func made[T any](n int) []T { return make([]T, n, n+3) }

func main() {
	fmt.Println(len(made[int](2)), cap(made[int](2)))
	fmt.Println(len(made[string](1)), cap(made[string](1)))
	local := func() []byte { return make([]byte, 1, 5) }
	fmt.Println(len(local()), cap(local()))
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceCollectionConstantCapacity(t *testing.T) {
	for name, source := range map[string]string{
		"conversion_operand_once":             `package main;import "fmt";func text()string{fmt.Println("text");return "abc"};func main(){b:=[]byte(text());r:=[]rune(text());fmt.Println(b,len(b),cap(b));fmt.Println(r,len(r),cap(r))}`,
		"literal_constant_dynamic":            `package main;import "fmt";func text()string{return "hey"};func main(){a:=[]byte("hey");const s="hey";b:=[]byte(s);c:=[]byte("h"+"ey");d:=[]byte(text());fmt.Println(a,len(a),cap(a));fmt.Println(b,len(b),cap(b));fmt.Println(c,len(c),cap(c));fmt.Println(d,len(d),cap(d))}`,
		"literal_alias_and_unicode":           `package main;import "fmt";type Byte=byte;type Bytes []Byte;func main(){a:=Bytes("é𝄞");b:=[]rune("é𝄞");c:=[]byte("");fmt.Printf("%T %v %d %d\n",a,a,len(a),cap(a));fmt.Println(b,len(b),cap(b));fmt.Println(c==nil,len(c),cap(c))}`,
		"native_read_keeps_converted_backing": `package main;import("fmt";"strings");func text()string{return "abc"};func main(){b:=[]byte(text());full:=b[:cap(b)];r:=strings.NewReader("XYZ");n,e:=r.Read(full[1:4]);fmt.Println(n,e,b,full,len(b),cap(b));b=append(b,'Q');fmt.Println(full,b)}`,
		"literal_append_allocates":            `package main;import "fmt";func main(){b:=[]byte("abc");alias:=b;b=append(b,'d');b[0]='X';fmt.Println(string(alias),string(b),cap(alias),cap(b))}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceCollectionConstantCapacityTypedJSON(t *testing.T) {
	const source = `package main;func main(){b:=[]byte("abc");println(len(b),cap(b))}`
	p, err := gosource.Parse(strings.NewReader(source), "constant.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := (typedjson.EncodeOptions{}).Encode(&data, p.File); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(&data)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	syntax.Walk(node, func(n syntax.Node) bool {
		if x, ok := n.(*syntax.BashPPConvertExpr); ok && x.GoStringConstant {
			found++
		}
		return true
	})
	if found != 1 {
		t.Fatalf("constant conversion metadata count %d", found)
	}
	var output bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &output))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.Run(ctx, node); err != nil {
		t.Fatal(err)
	}
	if output.String() != "3 3\n" {
		t.Fatalf("roundtrip capacity %q", output.String())
	}
}
