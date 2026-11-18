package interp_test

import (
	"strings"
	"testing"
	"time"
)

func TestJobCarrierBuiltinPidReadyBug(t *testing.T) {
	c := new(testCarrier)

	start := time.Now()
	// 10 probes of $! against a background function.
	out := runCarrierScript(t, c, `
f() {
	while true; do read -t 0.1 || true; done
	echo "func exited"
}
f &
for i in 1 2 3 4 5 6 7 8 9 10; do
	echo "probe $i: $SECONDS $!"
	kill -0 $! 2>/dev/null || true
done
kill -9 %1
wait %1
echo ok
`)
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("evaluating $! for background function took too long (%v), likely hitting 250ms timeouts\nOutput:\n%s", elapsed, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("unexpected output (took %v):\n%s", elapsed, out)
	}
}
