//go:build full

package interp_test

import "testing"

func TestS374BisectedIssue32187RecoveredFunctionCell(t *testing.T) {
	const source = `package main
type check struct { name string; f func() }
func shouldPanic(f func()) { defer func() { _ = recover() }(); f() }
func main() {
	var x any
	var s []int
	checks := []check{
		{"assert", func() { _ = x.(*int) }},
		{"bounds", func() { _ = x == s[1] }},
	}
	for _, tc := range checks { shouldPanic(tc.f) }
}`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	if got.stdout != "" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v", got)
	}
}

func TestS374BisectedIssue37975RecoveredFunctionCell(t *testing.T) {
	const source = `package main
type check struct { f func(); want string }
func shouldPanic(f func()) { defer func() { _ = recover() }(); f() }
func main() {
	checks := []check{
		{func() { n := 2; _ = make([]int, n, 1) }, "cap out of range"},
		{func() { n := -1; _ = make([]int, n, 3) }, "len out of range"},
	}
	for _, tc := range checks { shouldPanic(tc.f) }
}`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	if got.stdout != "" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v", got)
	}
}

func TestS374BisectedZeroDivideRecoveredFunctionCell(t *testing.T) {
	const source = `package main
type check struct { fn func() }
func run(fn func()) { defer func() { _ = recover() }(); fn() }
func main() {
	zero := 0
	checks := []check{
		{func() { _ = 1 / zero }},
		{func() { _ = 2 / zero }},
	}
	for _, tc := range checks { run(tc.fn) }
}`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	if got.stdout != "" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v", got)
	}
}
