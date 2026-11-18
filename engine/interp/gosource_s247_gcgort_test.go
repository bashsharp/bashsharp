//go:build full

package interp_test

// Sprint: #247; Story: #673; Story-ID: f24307569417

import "testing"

// test/gcgort.go multiplies complex64 values held behind a pointer, in array
// and slice elements, in a struct field and in an interface until they
// overflow to (+Inf-0i). A compound update through any addressed target, and
// a scalar boxed into an interface, must keep the runtime IEEE complex/float
// carrier: the target's stored spelling is read back as the declared complex
// type, and a non-finite result (which has no exact constant) commits its
// carrier instead of dereferencing the absent constant.
func TestS247GcgortComplexUpdateKeepsCarrier(t *testing.T) {
	const source = `package main

import "fmt"

type box struct{ z complex64 }

func main() {
	p := new(complex64)
	*p = complex64(complex(float32(1.01), float32(1.01)))
	arr := [2]complex64{complex64(complex(float32(1.01), float32(1.01))), 1}
	s := make([]complex64, 1)
	s[0] = arr[0]
	b := box{z: arr[0]}
	var iface interface{} = arr[0]
	for i := 0; i < 8; i++ {
		*p *= complex(real(*p)*1.01, imag(*p)*1.01)
		arr[0] *= complex(real(arr[0])*1.01, imag(arr[0])*1.01)
		s[0] *= complex(real(s[0])*1.01, imag(s[0])*1.01)
		b.z *= complex(real(b.z)*1.01, imag(b.z)*1.01)
		iface = iface.(complex64) * complex(real(iface.(complex64))*1.01, imag(iface.(complex64))*1.01)
	}
	fmt.Println(*p, arr[0], s[0], b.z, iface)
	f := [1]float32{3e38}
	f[0] *= 10
	var fi interface{} = float32(3e38)
	fi = fi.(float32) * -10
	fmt.Println(f[0], fi)
	c := [2]complex64{1 + 2i, 3}
	c[0] *= 2 - 1i
	c[1] += 0.5i
	fmt.Println(c)
}
`
	const want = "(+Inf-0i) (+Inf-0i) (+Inf-0i) (+Inf-0i) (+Inf-0i)\n+Inf -Inf\n[(4+3i) (3+0.5i)]\n"
	out, stderr, err := runGoSource(t, "gcgort-complex-update", source)
	if err != nil || stderr != "" || out != want {
		t.Fatalf("run=%v\nstdout=%q\nwant  =%q\nstderr=%q", err, out, want, stderr)
	}
}

// Control: finite float and integer element updates keep their exact
// results, and a boxed runtime value still refuses an assertion to the wrong
// dynamic type with Go's recoverable interface-conversion panic.
func TestS247GcgortFiniteUpdateAndAssertionRefusal(t *testing.T) {
	const source = `package main

import "fmt"

func main() {
	f := []float32{1.5}
	f[0] *= 1.1
	n := [1]int{7}
	n[0] *= 6
	var boxed interface{} = f[0] * 2
	fmt.Println(f[0], n[0], boxed)
	defer func() { fmt.Println("recovered:", recover()) }()
	_ = boxed.(float64)
}
`
	const want = "1.6500001 42 3.3000002\nrecovered: interface conversion: interface {} is float32, not float64\n"
	out, stderr, err := runGoSource(t, "gcgort-finite-control", source)
	if err != nil || stderr != "" || out != want {
		t.Fatalf("run=%v\nstdout=%q\nwant  =%q\nstderr=%q", err, out, want, stderr)
	}
}
