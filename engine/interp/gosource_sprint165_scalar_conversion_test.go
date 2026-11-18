// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

// GoSource stores scalar cells as shell strings, but their declared Go type
// must survive through named scalar and byte-slice conversions.
func TestGoSourceSprint165NamedScalarAndByteConversions(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

type Octet uint8
type Octets []Octet
type Text string

func main() {
	var n Octet = 65
	b := Octets("AZ")
	c := Octets(b)
	s := Text(c)
	fmt.Printf("%T %d %T %d %q %T %q\n", n, n, c, c[1], s, s, string(c))
}
`, nil, "")
}

func TestGoSourceSprint165TypedStringThroughInterfaceConverts(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

type Text string
type Octets []byte

func main() {
	var boxed any = Text("AZ")
	text := boxed.(Text)
	bytes := Octets(text)
	fmt.Printf("%T %q %T %d %q\n", text, text, bytes, bytes[1], string(bytes))
}
`, nil, "")
}

// An integer-to-string conversion uses the integer's rune value. A typed
// uint64 is stored as text in the interpreter, so it must not first be
// constrained to int64; Go converts both of these values to U+FFFD.
func TestGoSourceSprint165WideNamedIntegerStringConversion(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

type Glyph uint64

func wide() Glyph { return 1 << 63 }

func main() {
	fmt.Printf("%q %q\n", string(Glyph(0x110000)), string(wide()))
}
`, nil, "")
}

// An interface comparison is checked statically, then its dynamic value
// decides whether execution panics. In particular, two interface values that
// both contain maps are valid Go expressions but panic at runtime.
func TestGoSourceSprint165UncomparableInterfaceComparison(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

func compared(x, y any) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	return x == y
}

func main() {
	var maps any = map[string]int{"one": 1}
	var numbers any = 1
	fmt.Println(compared(maps, maps), compared(numbers, numbers))
}
`, nil, "")
}
