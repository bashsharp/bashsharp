// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build darwin

package interp

import "testing"

func TestDarwinKillSignalMappings(t *testing.T) {
	for _, test := range []struct {
		name string
		num  int
	}{
		{"EMT", 7},
		{"INFO", 29},
		{"USR1", 30},
	} {
		sig, ok := signalByName(test.name)
		if !ok || int(sig) != test.num {
			t.Errorf("signalByName(%q) = %d, %v; want %d, true", test.name, sig, ok, test.num)
		}
		_, got, ok := signalByNumber(test.num)
		if !ok || got != test.name {
			t.Errorf("signalByNumber(%d) = %q, %v; want %q, true", test.num, got, ok, test.name)
		}
	}
}
