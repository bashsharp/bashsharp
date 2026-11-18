// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import "testing"

// The strictPosix flag is the foundation for the strict-sh conformance
// behaviors (yash -p): it must survive both Reset and Subshell, since the
// yash cases exercise strict semantics inside subshells and across runs.
func TestStrictPosixPropagation(t *testing.T) {
	r, err := New(WithStrictPosix(true))
	if err != nil {
		t.Fatal(err)
	}
	if !r.strictPosix {
		t.Fatal("WithStrictPosix(true) did not set strictPosix")
	}
	r.Reset()
	if !r.strictPosix {
		t.Fatal("Reset dropped strictPosix")
	}
	sub := r.Subshell()
	if !sub.strictPosix {
		t.Fatal("Subshell dropped strictPosix")
	}
}

func TestStrictPosixXpgEchoDefaultFollowsOptionOrder(t *testing.T) {
	tests := []struct {
		name       string
		opts       []RunnerOption
		wantStrict bool
		wantXpg    bool
	}{
		{name: "bash_default"},
		{name: "strict_sh", opts: []RunnerOption{WithStrictPosix(true)}, wantStrict: true, wantXpg: true},
		{name: "strict_then_bash", opts: []RunnerOption{WithStrictPosix(true), WithStrictPosix(false)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, err := New(test.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if r.strictPosix != test.wantStrict {
				t.Fatalf("strictPosix = %v, want %v", r.strictPosix, test.wantStrict)
			}
			xpgEcho, supported := r.bashOptByName("xpg_echo")
			if !supported || xpgEcho == nil {
				t.Fatal("xpg_echo option is unavailable")
			}
			if *xpgEcho != test.wantXpg {
				t.Fatalf("xpg_echo = %v, want %v", *xpgEcho, test.wantXpg)
			}
		})
	}
}
