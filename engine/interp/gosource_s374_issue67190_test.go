//go:build full

package interp_test

import "testing"

func TestS374BisectedIssue67190ChannelDirectionComparison(t *testing.T) {
	const source = `package main
func main() {
	ch := make(chan struct{})
	var receive <-chan struct{} = ch
	switch ch { case receive: default: panic("narrow") }
	switch receive { case ch: default: panic("wide") }
}`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	if got.stdout != "" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v", got)
	}
}
