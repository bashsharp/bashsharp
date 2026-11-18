package interp_test

import (
	"strings"
	"testing"
)

func TestBashPPStory201NamedCompositeClosure(t *testing.T) {
	tests := []struct {
		name, src, wantOut, wantErr string
		wantExit                    bool
	}{
		{name: "defined array zero assign index", src: "type Names [2]string\nfunc main() {\n var n Names\n n[0] = \"a\"\n printf '%s:%s\\n' n[0] n[1]\n}\nmain()\n", wantOut: "a:\n"},
		{name: "defined slice literal index", src: "type Ints []int\nfunc main() {\n v := Ints{1, 2}\n printf '%s\\n' v[1]\n}\nmain()\n", wantOut: "2\n"},
		{name: "defined map zero nil", src: "type Env map[string]int\nfunc main() {\n var e Env\n if e == nil { printf 'nil\\n' } else { printf 'nonnil\\n' }\n}\nmain()\n", wantOut: "nil\n"},
		{name: "alias slice literal", src: "type Ints = []int\nfunc main() {\n v := Ints{1, 2}\n printf '%s\\n' v[0]\n}\nmain()\n", wantOut: "1\n"},
		{name: "alias chain preserves defined identity", src: "type Ints []int\ntype More = Ints\nfunc main() {\n a := Ints{3}\n var b More = a\n printf '%s\\n' b[0]\n}\nmain()\n", wantOut: "3\n"},
		{name: "alias named struct literal", src: "type Point struct { X int }\ntype P = Point\nfunc main() {\n p := P{X: 3}\n printf '%s\\n' p.X\n}\nmain()\n", wantOut: "3\n"},
		{name: "defined struct selector", src: "type Point struct { X int }\ntype Q Point\nfunc main() {\n var q Q\n q.X = 4\n printf '%s\\n' q.X\n}\nmain()\n", wantOut: "4\n"},
		{name: "new named struct selector autoderef", src: "type Point struct { X int }\nfunc main() {\n p := new(Point)\n p.X = 7\n x := p.X\n printf '%s\\n' x\n}\nmain()\n", wantOut: "7\n"},
		{name: "defined pointer zero and mutation", src: "type Ptr *int\nfunc main() {\n var z Ptr\n if z == nil { printf 'nil\\n' }\n x := 1\n p := &x\n var q Ptr = p\n *q = 4\n printf '%s\\n' x\n}\nmain()\n", wantOut: "nil\n4\n"},
		{name: "single letter defined pointer", src: "type P *int\nfunc main() {\n x := 1\n raw := &x\n var p P = raw\n *p = 9\n printf '%s\\n' x\n}\nmain()\n", wantOut: "9\n"},
		{name: "defined pointer reassignment from new", src: "type P *int\nfunc main() {\n var p P\n p = new(int)\n *p = 9\n got := *p\n printf '%s\\n' got\n}\nmain()\n", wantOut: "9\n"},
		{name: "alias pointer assignment", src: "type Ptr *int\ntype Alias = Ptr\nfunc main() {\n x := 1\n p := &x\n var q Alias = p\n *q = 5\n printf '%s\\n' x\n}\nmain()\n", wantOut: "5\n"},
		{name: "distinct defined pointer assignment rejected", src: "type Ptr *int\ntype Other *int\nfunc main() {\n var p Ptr\n var q Other = p\n}\nmain()\n", wantErr: "BASHPP-EASSIGN-MISMATCH", wantExit: true},
		{name: "new named array", src: "type Pair [2]int\nfunc main() {\n p := new(Pair)\n a := *p\n a[1] = 8\n printf '%s\\n' a[1]\n}\nmain()\n", wantOut: "8\n"},
		{name: "new named slice zero", src: "type Ints []int\nfunc main() {\n p := new(Ints)\n if *p == nil { printf 'nil\\n' } else { printf 'nonnil\\n' }\n}\nmain()\n", wantOut: "nil\n"},
		{name: "new named map zero", src: "type Env map[string]int\nfunc main() {\n p := new(Env)\n if *p == nil { printf 'nil\\n' } else { printf 'nonnil\\n' }\n}\nmain()\n", wantOut: "nil\n"},
		{name: "new named pointer zero", src: "type Ptr *int\nfunc main() {\n pp := new(Ptr)\n p := *pp\n if p == nil { printf 'nil\\n' } else { printf 'nonnil\\n' }\n}\nmain()\n", wantOut: "nil\n"},
		{name: "pointer selector assignment autoderef", src: "type Point struct { X int }\nfunc main() {\n v := Point{X: 1}\n p := &v\n p.X = 9\n printf '%s\\n' v.X\n}\nmain()\n", wantOut: "9\n"},
		{name: "pointer index autoderef", src: "func main() {\n s := []int{1, 2}\n p := &s\n x := p[1]\n printf '%s\\n' x\n}\nmain()\n", wantOut: "2\n"},
		{name: "pointer slice autoderef", src: "func main() {\n a := [3]int{1, 2, 3}\n p := &a\n q := p[0:2]\n printf '%s\\n' q[1]\n}\nmain()\n", wantOut: "2\n"},
		{name: "constant expression array length", src: "func main() {\n a := [2+1]int{1, 2, 3}\n printf '%s\\n' a[2]\n}\nmain()\n", wantOut: "3\n"},
		{name: "named constant expression array length", src: "type Pair [1+1]int\nfunc main() {\n var p Pair\n printf '%s\\n' p[1]\n}\nmain()\n", wantOut: "0\n"},
		{name: "declared constant array length", src: "const N = 4\ntype Four [N]int\nfunc main() {\n var a Four\n a[3] = 7\n printf '%s\\n' a[3]\n}\nmain()\n", wantOut: "7\n"},
		{name: "inferred array compatible with fixed length", src: "func main() {\n var a [3]int = [...]int{1, 2, 3}\n printf '%s\\n' a[2]\n}\nmain()\n", wantOut: "3\n"},
		{name: "inferred array length mismatch rejected", src: "func main() {\n var a [2]int = [...]int{1, 2, 3}\n}\nmain()\n", wantErr: "BASHPP-ECOLLECTION-LENGTH", wantExit: true},
		{name: "named array equality", src: "type A3 [3]int\nfunc main() {\n a := A3{1, 2, 3}\n b := A3{1, 2, 3}\n if a == b { printf 'eq\\n' } else { printf 'ne\\n' }\n}\nmain()\n", wantOut: "eq\n"},
		{name: "named slice nil equality", src: "type Ints []int\nfunc main() {\n var v Ints\n if v == nil { printf 'nil\\n' } else { printf 'nonnil\\n' }\n}\nmain()\n", wantOut: "nil\n"},
		{name: "named map range", src: "type Env map[string]int\nfunc main() {\n e := Env{\"a\": 1}\n for k, v := range e { printf '%s=%s\\n' \"$k\" \"$v\" }\n}\nmain()\n", wantOut: "a=1\n"},
		{name: "named slice range", src: "type Ints []int\nfunc main() {\n v := Ints{5, 6}\n for i, x := range v { printf '%s:%s\\n' \"$i\" \"$x\" }\n}\nmain()\n", wantOut: "0:5\n1:6\n"},
		{name: "range dereferenced slice", src: "func main() {\n s := []int{4, 5}\n p := &s\n for i, v := range *p { printf '%s:%s\\n' \"$i\" \"$v\" }\n}\nmain()\n", wantOut: "0:4\n1:5\n"},
		{name: "range selected slice", src: "type Cfg struct { Ports []int }\nfunc main() {\n c := Cfg{Ports: []int{3, 4}}\n for i, v := range c.Ports { printf '%s:%s\\n' \"$i\" \"$v\" }\n}\nmain()\n", wantOut: "0:3\n1:4\n"},
		{name: "composite literal index", src: "func main() {\n x := [3]int{7, 8, 9}[1]\n printf '%s\\n' x\n}\nmain()\n", wantOut: "8\n"},
		{name: "indexed array equality", src: "type Box struct { Items [][2]int }\nfunc main() {\n b := Box{Items: [][2]int{[2]int{1, 2}}}\n got := b.Items[0]\n want := [2]int{1, 2}\n if got == want { printf 'eq\\n' } else { printf 'ne\\n' }\n}\nmain()\n", wantOut: "eq\n"},
		{name: "selected struct equality", src: "type Point struct { X int }\ntype Box struct { P Point }\nfunc main() {\n b := Box{P: Point{X: 1}}\n got := b.P\n want := Point{X: 1}\n if got == want { printf 'eq\\n' } else { printf 'ne\\n' }\n}\nmain()\n", wantOut: "eq\n"},
		{name: "selected slice operand", src: "type Cfg struct { Ports []int }\nfunc main() {\n c := Cfg{Ports: []int{1, 2, 3}}\n s := c.Ports[1:3]\n printf '%s\\n' s[0]\n}\nmain()\n", wantOut: "2\n"},
		{name: "unnamed to named slice assignment", src: "type Ints []int\nfunc main() {\n var v Ints = []int{1, 2}\n printf '%s\\n' v[1]\n}\nmain()\n", wantOut: "2\n"},
		{name: "named to unnamed slice assignment", src: "type Ints []int\nfunc main() {\n v := Ints{1, 2}\n var u []int = v\n printf '%s\\n' u[0]\n}\nmain()\n", wantOut: "1\n"},
		{name: "slice equality rejected", src: "func main() {\n a := []int{1}\n b := []int{1}\n if a == b { printf 'eq\\n' }\n}\nmain()\n", wantErr: "BASHPP-ECOMPARE-NONCOMPARABLE", wantExit: true},
		{name: "distinct defined slice assignment rejected", src: "type A []int\ntype B []int\nfunc main() {\n a := A{1}\n var b B = a\n}\nmain()\n", wantErr: "BASHPP-ECOLLECTION-ELEMENT: cannot use value as B", wantExit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tt.src)
			if (err != nil) != tt.wantExit {
				t.Fatalf("err = %v, wantExit %v; stderr=%q", err, tt.wantExit, stderr)
			}
			if out != tt.wantOut {
				t.Fatalf("stdout = %q, want %q; stderr=%q", out, tt.wantOut, stderr)
			}
			if tt.wantErr == "" {
				if stderr != "" {
					t.Fatalf("stderr = %q, want empty", stderr)
				}
			} else if !strings.Contains(stderr, tt.wantErr) {
				t.Fatalf("stderr = %q, want substring %q", stderr, tt.wantErr)
			}
		})
	}
}
