// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The carrier's terminal wait must release both the Go process handle (a
// pidfd on Linux) and exec.Cmd's parent-owned pipes. Count the shell's own
// descriptors, not the test process's, after a sequence of external commands.
func TestCarrierExternalCommandsKeepFDsBounded(t *testing.T) {
	const commands = 200
	script := fmt.Sprintf(`
count_fds() { /bin/ls /proc/$$/fd | /usr/bin/wc -l; }
printf 'before='
count_fds
i=0
while [ "$i" -lt %d ]; do
    /usr/bin/true &
    wait "$!"
    i=$((i + 1))
done
printf 'after='
count_fds
`, commands)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtBashBin(t), "-c", script)
	cmd.Dir = t.TempDir()
	// Finalizers can hide an ownership leak in a short test. The shell must
	// close every descriptor itself, even when GC never runs.
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + cmd.Dir, "GOGC=off"}
	output, err := cmd.CombinedOutput()
	out := string(output)
	if ctx.Err() != nil || err != nil {
		t.Fatalf("shell failed: %v (context: %v): %s", err, ctx.Err(), out)
	}
	var before, after int
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, "before="); ok {
			before, _ = strconv.Atoi(strings.TrimSpace(value))
		}
		if value, ok := strings.CutPrefix(line, "after="); ok {
			after, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if before == 0 || after == 0 {
		t.Fatalf("missing descriptor counts: %q", out)
	}
	t.Logf("shell fds before=%d after=%d", before, after)
	if growth := after - before; growth > 8 {
		t.Fatalf("shell leaked %d descriptors across %d external commands (before=%d, after=%d)", growth, commands, before, after)
	}
}
