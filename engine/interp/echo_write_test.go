package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type echoWriteRecorder struct {
	writes []string
}

func (w *echoWriteRecorder) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

// VSC's SigWait helper shares stderr with a shell `echo exitstatus 0`.
// One write keeps another process's line from splitting that result.
func TestEchoWritesCompleteResultAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
	}{
		{"plain", "echo exitstatus 0", "exitstatus 0\n"},
		{"posix", "set -o posix; echo exitstatus 0", "exitstatus 0\n"},
		{"no-newline", "echo -n exitstatus 0", "exitstatus 0"},
		{"escape-stop", `echo -e 'before\cafter'`, "before"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.script), "")
			if err != nil {
				t.Fatal(err)
			}
			output := &echoWriteRecorder{}
			runner, err := interp.New(interp.StdIO(nil, output, output))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if len(output.writes) != 1 || output.writes[0] != tc.want {
				t.Fatalf("writes = %#v; want one write of %q", output.writes, tc.want)
			}
		})
	}
}
