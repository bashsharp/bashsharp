// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestStartupOptionsFollowInteractiveMode(t *testing.T) {
	tests := []struct {
		name string
		opts []interp.RunnerOption
		want map[string]string
	}{
		{
			name: "noninteractive",
			want: map[string]string{
				"emacs": "off", "histexpand": "off", "history": "off", "monitor": "off",
			},
		},
		{
			name: "interactive",
			opts: []interp.RunnerOption{interp.Interactive(true)},
			want: map[string]string{
				"emacs": "on", "histexpand": "on", "history": "on", "monitor": "off",
			},
		},
		{
			name: "explicit overrides",
			opts: []interp.RunnerOption{
				interp.Interactive(true),
				interp.Params("+o", "history", "+o", "histexpand", "+o", "emacs", "-m"),
			},
			want: map[string]string{
				"emacs": "off", "histexpand": "off", "history": "off", "monitor": "on",
			},
		},
	}

	file, err := syntax.NewParser().Parse(strings.NewReader("set -o"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			opts := append([]interp.RunnerOption{interp.StdIO(nil, &stdout, nil)}, test.opts...)
			runner, err := interp.New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			got := make(map[string]string)
			for _, line := range strings.Split(stdout.String(), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 {
					got[fields[0]] = fields[1]
				}
			}
			for name, want := range test.want {
				if got[name] != want {
					t.Errorf("set -o %s = %q, want %q", name, got[name], want)
				}
			}
		})
	}
}
