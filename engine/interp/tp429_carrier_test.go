package interp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestTP429CarrierWaitAfterSignal(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			// Simple-call background: $! resolves to the exec'd child
			// PID after publishBgPid closes pidReady.
			name: "simple-kill-term",
			src: `set +m
sleep 10 &
pid=$!
kill -TERM $pid
wait $pid
echo "rc=$?"`,
			want: "rc=143",
		},
		{
			// Compound command: publishPidToBang is false, so $! is
			// the carrier PID — deterministic identity regardless of
			// timing. This exercises the carrier-watcher relay path
			// where the kill builtin targets a numeric PID that lives
			// inside bgProc.pids (the carrier PID).
			name: "compound-kill-term",
			src: `set +m
{ sleep 10; } &
pid=$!
kill -TERM $pid
wait $pid
echo "rc=$?"`,
			want: "rc=143",
		},
		{
			// SIGKILL is uncatchable; the exit status must be
			// 128+9=137 regardless of signal disposition.
			name: "compound-kill-kill",
			src: `set +m
{ sleep 10; } &
pid=$!
kill -KILL $pid
wait $pid
echo "rc=$?"`,
			want: "rc=137",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := new(testCarrier)
			file, err := syntax.NewParser().Parse(strings.NewReader(tt.src), "")
			if err != nil {
				t.Fatal(err)
			}
			var buf concBuffer
			r, err := interp.New(
				interp.WithJobCarrier(c),
				interp.StdIO(nil, &buf, &buf),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			runErr := r.Run(ctx, file)
			out := strings.TrimSpace(buf.String())
			t.Logf("output: %q, runErr: %v", out, runErr)
			if out != tt.want {
				t.Errorf("expected %q, got %q", tt.want, out)
			}
		})
	}
}
