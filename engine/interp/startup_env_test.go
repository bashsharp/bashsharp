// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestPosixStartupExportAttributes(t *testing.T) {
	tests := []struct {
		name                string
		env                 expand.Environ
		wantPosixlyExported bool
	}{
		{
			name: "synthesized POSIXLY_CORRECT is a shell variable",
			env:  expand.ListEnviron(),
		},
		{
			name:                "inherited POSIXLY_CORRECT stays exported",
			env:                 expand.ListEnviron("POSIXLY_CORRECT=custom"),
			wantPosixlyExported: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var pwd, posixly expand.Variable
			capture := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					env := interp.HandlerCtx(ctx).Env
					pwd = env.Get("PWD")
					posixly = env.Get("POSIXLY_CORRECT")
					return nil
				}
			}
			runner, err := interp.New(
				interp.Env(test.env),
				interp.WithPosixMode(true),
				interp.ExecHandlers(capture),
			)
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader("probe"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}

			if !pwd.IsSet() || !pwd.Exported {
				t.Fatalf("PWD = %#v; want a set, exported startup variable", pwd)
			}
			if !posixly.IsSet() || posixly.Exported != test.wantPosixlyExported {
				t.Fatalf("POSIXLY_CORRECT = %#v; want set and exported=%v", posixly, test.wantPosixlyExported)
			}
		})
	}
}
