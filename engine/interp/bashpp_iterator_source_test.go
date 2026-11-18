package interp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// CPython generator close/finally plus Go os/exec bounded delivery lifecycle;
// provenance is in plan-story576-python-stream-checkpoint.md. The assertion
// measures actual worker progress while the B14 consumer is deliberately idle.
func TestForeignStreamingBoundedAndClose(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	runtime := polyglot.Python{Command: python}
	plans, err := polyglot.Prepare(t.Context(), []polyglot.Block{{Language: "python", Source: `def state(n: int = -1) -> int:
    if n>=0: state.n=n
    return getattr(state,'n',0)
def values() -> Iterator[int]:
    try:
        while True:
            state(state()+1)
            yield state()
    finally: state(9999)
`}}, map[string]polyglot.Analyzer{"python": runtime})
	if err != nil {
		t.Fatal(err)
	}
	module := polyglot.Start(plans[0], runtime)
	defer module.Close()
	process, err := StartForeignIterator(t.Context(), module, "values", nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	deadline := time.Now().Add(3 * time.Second)
	var produced int64
	for time.Now().Before(deadline) {
		got, err := module.Call(t.Context(), "state")
		if err != nil {
			t.Fatal(err)
		}
		produced = got.Value.(int64)
		if produced >= bashPPDefaultLineBuffer {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	got, err := module.Call(t.Context(), "state")
	if err != nil {
		t.Fatal(err)
	}
	produced = got.Value.(int64)
	if produced < bashPPDefaultLineBuffer || produced > bashPPDefaultLineBuffer+2 {
		t.Fatalf("unbounded or stalled producer: %d", produced)
	}
	process.Close()
	got, err = module.Call(t.Context(), "state")
	if err != nil || got.Value != int64(9999) {
		t.Fatalf("generator not closed in persistent worker: %+v %v", got, err)
	}
	status, err := process.Wait()
	status2, err2 := process.Wait()
	if status != status2 || err != err2 {
		t.Fatal("wait not exact once")
	}
}

func TestForeignStreamingBlockedNextCancel(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	runtime := polyglot.Python{Command: python}
	plans, err := polyglot.Prepare(t.Context(), []polyglot.Block{{Language: "python", Source: "def values(marker: str) -> Iterator[int]:\n    import time\n    yield 0\n    open(marker, 'w').close()\n    time.sleep(30)\n    yield 1\n"}}, map[string]polyglot.Analyzer{"python": runtime})
	if err != nil {
		t.Fatal(err)
	}
	module := polyglot.Start(plans[0], runtime)
	defer module.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "next-entered")
	process, err := StartForeignIterator(ctx, module, "values", []any{marker}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	// Observe worker startup and its first value before cancellation; native
	// process launch under the Windows race detector can exceed 100 ms.
	if _, ok := <-process.Lines(); !ok {
		t.Fatal("worker closed before first value")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never entered blocking next")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	for range process.Lines() {
	}
	if _, err = process.Wait(); err == nil {
		t.Fatal("cancelled worker reported success")
	}
}
