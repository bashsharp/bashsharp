// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"slices"
	"testing"
)

func TestSetExecEnvValue(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{name: "append", env: []string{"A=1"}, want: []string{"A=1", "_=/bin/probe"}},
		{name: "replace", env: []string{"_=parent", "A=1"}, want: []string{"_=/bin/probe", "A=1"}},
		{
			name: "replace and deduplicate",
			env:  []string{"_=inherited", "A=1", "_=overlay"},
			want: []string{"_=/bin/probe", "A=1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := setExecEnvValue(test.env, "_", "/bin/probe"); !slices.Equal(got, test.want) {
				t.Fatalf("setExecEnvValue() = %q, want %q", got, test.want)
			}
		})
	}
}
