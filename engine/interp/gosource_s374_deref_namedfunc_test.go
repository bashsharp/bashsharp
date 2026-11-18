//go:build full

package interp_test

// Sprint: #374; Story: #1527; Story-ID: dd26e98fbeee

import "testing"

// Dereferencing a pointer to a named function type yields the function
// value itself, which compares with nil like any other func value: a live
// one is non-nil, a zero one is nil. The comparison must see through the
// named type to its func underlying, exactly as it does for a variable of
// that type.
func TestS374DerefNamedFuncNilCompare(t *testing.T) {
	const source = `package main

import "fmt"

type Qualifier func() string

func qual() string { return "q" }

func main() {
	var q Qualifier = qual
	s := &q
	fmt.Println("live-ne:", *s != nil)
	var z Qualifier
	sz := &z
	fmt.Println("nil-eq:", *sz == nil)
	switch *sz {
	case nil:
		fmt.Println("switch: nil")
	default:
		fmt.Println("switch: live")
	}
	f := qual
	pf := &f
	fmt.Println("unnamed-ne:", *pf != nil)
}
`
	got, err := runGoSourceIdentity(t, source, "")
	want := "live-ne: true\nnil-eq: true\nswitch: nil\nunnamed-ne: true\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
