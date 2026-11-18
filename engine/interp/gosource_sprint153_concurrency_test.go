//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
)

func TestSprint153PipelineSelectCancel(t *testing.T) {
	out, errout, err := runSprint153Concurrency(t, "pipeline_select_cancel.go")
	if err != nil || out != "2\n4\ndone 6\n" || errout != "" {
		t.Fatalf("run: err=%v stdout=%q stderr=%q", err, out, errout)
	}
}

func TestSprint153ChannelDeadlockReports(t *testing.T) {
	out, errout, err := runSprint153Concurrency(t, "deadlock_negative.go.txt")
	if status, ok := interp.IsExitStatus(err); !ok || status != 2 || out != "" || !strings.Contains(errout, "all goroutines are asleep - deadlock!") {
		t.Fatalf("deadlock: err=%v stdout=%q stderr=%q", err, out, errout)
	}
}
