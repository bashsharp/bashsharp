// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import "testing"

// The hard-ignore bridge carries both kinds of ignore, because execve does
// not distinguish them: a signal this shell set to SIG_IGN and one that was
// already SIG_IGN when it started are equally inherited by the process an
// exec replaces it with. Carrying only the first lost an inherited ignore
// at the second hop of a chain of execs.
func TestStory687HardIgnoreEnvValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		set     map[string]bool
		startup map[string]bool
		want    string
	}{
		{"none", nil, nil, ""},
		{"set", map[string]bool{"TERM": true}, nil, "TERM"},
		{"inherited", nil, map[string]bool{"TERM": true}, "TERM"},
		{"both", map[string]bool{"USR1": true}, map[string]bool{"TERM": true}, "TERM,USR1"},
		{"overlap", map[string]bool{"TERM": true}, map[string]bool{"TERM": true}, "TERM"},
		{"sorted", map[string]bool{"USR2": true, "HUP": true}, map[string]bool{"TERM": true}, "HUP,TERM,USR2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{sigIgnored: tc.set, startupIgnored: tc.startup}
			if got := r.hardIgnoreEnvValue(); got != tc.want {
				t.Errorf("hardIgnoreEnvValue() = %q, want %q", got, tc.want)
			}
		})
	}
}
