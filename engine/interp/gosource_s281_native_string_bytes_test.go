//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0

import "testing"

func TestS281NativeCallbackStringPreservesBytes(t *testing.T) {
	const source = `package main

import "fmt"

type blob struct{ text string }

func (b blob) String() string { return b.text }

type props struct {
	flags   uint64
	params  []uint64
	results []uint64
}

func writeULEB(out []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		out = append(out, c)
		if v == 0 {
			return out
		}
	}
}

func readULEB(in []byte) (uint64, []byte) {
	var value uint64
	var shift uint
	for {
		b := in[0]
		in = in[1:]
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, in
		}
		shift += 7
	}
}

func serialize(p props) string {
	var out []byte
	out = writeULEB(out, p.flags)
	out = writeULEB(out, uint64(len(p.params)))
	for _, flag := range p.params {
		out = writeULEB(out, flag)
	}
	out = writeULEB(out, uint64(len(p.results)))
	for _, flag := range p.results {
		out = writeULEB(out, flag)
	}
	return string(out)
}

func deserialize(s string) props {
	in := []byte(s)
	var p props
	p.flags, in = readULEB(in)
	var n uint64
	n, in = readULEB(in)
	p.params = make([]uint64, n)
	for i := range p.params {
		p.params[i], in = readULEB(in)
	}
	n, in = readULEB(in)
	p.results = make([]uint64, n)
	for i := range p.results {
		p.results[i], in = readULEB(in)
	}
	return p
}

func main() {
	one := fmt.Sprint(blob{serialize(props{flags: 0xfffff})})
	fmt.Printf("flags %x %x\n", []byte(one), deserialize(one).flags)

	all := fmt.Sprint(blob{serialize(props{
		flags:   1,
		params:  []uint64{0x99, 0xaa, 0xfffff},
		results: []uint64{0xfeedface},
	})})
	round := deserialize(all)
	fmt.Printf("props %x %x %x %x\n", []byte(all), round.flags, round.params, round.results)

	invalid := fmt.Sprint(blob{string([]byte{'A', 0, 0xff, 0xfe, 'Z'})})
	fmt.Printf("invalid %x %v %d\n", []byte(invalid), invalid == string([]byte{'A', 0, 0xff, 0xfe, 'Z'}), len(invalid))

	unicode := fmt.Sprint(blob{"é\n\u2028\x01"})
	fmt.Printf("unicode %x %v %d\n", []byte(unicode), unicode == "é\n\u2028\x01", len(unicode))
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "flags ffff3f0000 fffff\n" +
		"props 01039901aa01ffff3f01cef5b7f70f 1 [99 aa fffff] [feedface]\n" +
		"invalid 4100fffe5a true 5\n" +
		"unicode c3a90ae280a801 true 7\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

func TestS281OriginalErrorAndPanicCallbackPreserveBytes(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"sort"
)

type originalError struct{ text string }

func (e originalError) Error() string { return e.text }

func checkError(name, text string) {
	got := fmt.Sprint(originalError{text})
	fmt.Printf("error %s %x %t %d\n", name, []byte(got), got == text, len(got))
}

func checkPanic(name, text string) {
	defer func() {
		got, ok := recover().(string)
		fmt.Printf("panic %s %x %t %d\n", name, []byte(got), ok && got == text, len(got))
	}()
	sort.Slice([]int{2, 1}, func(i, j int) bool { panic(text) })
}

func main() {
	invalid := string([]byte{'A', 0, 0xff, 0xfe, 'Z'})
	unicode := "é\n\u2028\x01"
	checkError("invalid", invalid)
	checkError("unicode", unicode)
	checkError("empty", "")
	checkPanic("invalid", invalid)
	checkPanic("unicode", unicode)
	checkPanic("empty", "")
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "error invalid 4100fffe5a true 5\n" +
		"error unicode c3a90ae280a801 true 7\n" +
		"error empty  true 0\n" +
		"panic invalid 4100fffe5a true 5\n" +
		"panic unicode c3a90ae280a801 true 7\n" +
		"panic empty  true 0\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}
