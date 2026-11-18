//go:build full

package interp_test

// Sprint: #374; Story-ID: df7e7d317cdd
import "testing"

// Go ranges over a value of any integer type, and the iteration variable takes
// that type. The bound is not always a named local: `*trials`, where trials is
// the *int64 flag.Int64 returned, reads a scalar out of dependency-owned
// storage, and that read arrives with type metadata the collection range path
// used to mistake for a non-collection ("cannot range over int64").
const goSourceRangeIntegerKinds = `package main

import (
	"flag"
	"fmt"
	"sync/atomic"
)

type ID int32
type Count uint16

const untyped = 2
const typed int8 = 2

type box struct {
	n   uint32
	id  ID
	ptr *int64
}

func count() int64 { return 2 }

var trials = flag.Int64("trials", 2, "n")
var utrials = flag.Uint64("utrials", 2, "n")
var itrials = flag.Int("itrials", 2, "n")
var big = flag.Uint64("big", 1<<63+5, "n")

func main() {
	flag.Parse()
	var i8 int8 = 2
	var i16 int16 = 2
	var i32 int32 = 2
	var i64 int64 = 2
	var u uint = 2
	var u8 uint8 = 2
	var u16 uint16 = 2
	var u32 uint32 = 2
	var u64 uint64 = 2
	var up uintptr = 2
	var id ID = 2
	var c Count = 2
	for i := range i8 {
		fmt.Printf("i8 %d %T\n", i, i)
	}
	for i := range i16 {
		fmt.Printf("i16 %d %T\n", i, i)
	}
	for i := range i32 {
		fmt.Printf("i32 %d %T\n", i, i)
	}
	for i := range i64 {
		fmt.Printf("i64 %d %T\n", i, i)
	}
	for i := range u {
		fmt.Printf("u %d %T\n", i, i)
	}
	for i := range u8 {
		fmt.Printf("u8 %d %T\n", i, i)
	}
	for i := range u16 {
		fmt.Printf("u16 %d %T\n", i, i)
	}
	for i := range u32 {
		fmt.Printf("u32 %d %T\n", i, i)
	}
	for i := range u64 {
		fmt.Printf("u64 %d %T\n", i, i)
	}
	for i := range up {
		fmt.Printf("up %d %T\n", i, i)
	}
	for i := range id {
		fmt.Printf("id %d %T\n", i, i)
	}
	for i := range c {
		fmt.Printf("c %d %T\n", i, i)
	}
	for i := range untyped {
		fmt.Printf("untyped %d %T\n", i, i)
	}
	for i := range typed {
		fmt.Printf("typed %d %T\n", i, i)
	}
	for i := range ID(2) {
		fmt.Printf("conv %d %T\n", i, i)
	}
	for i := range count() {
		fmt.Printf("call %d %T\n", i, i)
	}
	for i := range i64 + 1 {
		fmt.Printf("expr %d %T\n", i, i)
	}
	b := box{n: 2, id: 2, ptr: &i64}
	for i := range b.n {
		fmt.Printf("field %d %T\n", i, i)
	}
	for i := range b.id {
		fmt.Printf("fieldid %d %T\n", i, i)
	}
	for i := range *b.ptr {
		fmt.Printf("fieldptr %d %T\n", i, i)
	}
	xs := []uint8{2}
	for i := range xs[0] {
		fmt.Printf("elem %d %T\n", i, i)
	}
	p := &id
	for i := range *p {
		fmt.Printf("deref %d %T\n", i, i)
	}
	for i := range *trials {
		fmt.Printf("flag64 %d %T\n", i, i)
	}
	for i := range *utrials {
		fmt.Printf("flagu64 %d %T\n", i, i)
	}
	for i := range *itrials {
		fmt.Printf("flagint %d %T\n", i, i)
	}
	for i := range *big {
		fmt.Printf("big %d %T\n", i, i)
		if i == 1 {
			break
		}
	}
	var at atomic.Int64
	at.Store(2)
	for i := range at.Load() {
		fmt.Printf("atomic %d %T\n", i, i)
	}
	n := 0
	for range *trials {
		n++
	}
	for range u8 {
		n++
	}
	var zero int64
	neg := int64(-3)
	for i := range zero {
		fmt.Println("unreachable", i)
	}
	for i := range neg {
		fmt.Println("unreachable", i)
	}
	*trials = -1
	for i := range *trials {
		fmt.Println("unreachable", i)
	}
	*trials = 0
	for range *trials {
		n++
	}
	var sum ID
	for i := range id {
		sum += i
	}
	fmt.Println("n", n, "sum", sum)
}
`

const goSourceRangeIntegerKindsWant = `i8 0 int8
i8 1 int8
i16 0 int16
i16 1 int16
i32 0 int32
i32 1 int32
i64 0 int64
i64 1 int64
u 0 uint
u 1 uint
u8 0 uint8
u8 1 uint8
u16 0 uint16
u16 1 uint16
u32 0 uint32
u32 1 uint32
u64 0 uint64
u64 1 uint64
up 0 uintptr
up 1 uintptr
id 0 main.ID
id 1 main.ID
c 0 main.Count
c 1 main.Count
untyped 0 int
untyped 1 int
typed 0 int8
typed 1 int8
conv 0 main.ID
conv 1 main.ID
call 0 int64
call 1 int64
expr 0 int64
expr 1 int64
expr 2 int64
field 0 uint32
field 1 uint32
fieldid 0 main.ID
fieldid 1 main.ID
fieldptr 0 int64
fieldptr 1 int64
elem 0 uint8
elem 1 uint8
deref 0 main.ID
deref 1 main.ID
flag64 0 int64
flag64 1 int64
flagu64 0 uint64
flagu64 1 uint64
flagint 0 int
flagint 1 int
big 0 uint64
big 1 uint64
atomic 0 int64
atomic 1 int64
n 4 sum 1
`

func TestGoSourceRangeOverEveryIntegerType(t *testing.T) {
	out, stderr, err := runGoSource(t, "rangeint", goSourceRangeIntegerKinds)
	if err != nil || stderr != "" {
		t.Fatalf("err=%v stderr=%q stdout=%q", err, stderr, out)
	}
	if out != goSourceRangeIntegerKindsWant {
		t.Fatalf("stdout mismatch\n got: %q\nwant: %q", out, goSourceRangeIntegerKindsWant)
	}
}
