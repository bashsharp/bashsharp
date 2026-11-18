package interp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestCommandDeclarationAssignmentDoesNotSplit(t *testing.T) {
	for _, command := range []string{"command export", "command command export", "command readonly", "command command readonly"} {
		t.Run(command, func(t *testing.T) {
			src := "set -o posix; a='1  *  2'; " + command + " B=$a; printf '[%s]\\n' \"$B\""
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			r, err := interp.New(interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatalf("run: %v; output: %q", err, out.String())
			}
			if got := out.String(); got != "[1  *  2]\n" {
				t.Fatalf("output = %q", got)
			}
		})
	}
}
