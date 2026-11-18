//go:build full

package interp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Compile and execute the generated worker itself: checking the template text
// cannot catch a type error in its scratch allocation or a race in dispatch.
func s279CallArgsWorker(tb testing.TB, checkPool bool) (string, []string) {
	tb.Helper()
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{})
	if err != nil {
		tb.Fatal(err)
	}
	imports, mailbox := bashPPMailboxWorkerSource(false)
	source = strings.Replace(source, "//CALLBACKMAILBOXIMPORTS", imports, 1)
	source = strings.Replace(source, "//CALLBACKMAILBOX", mailbox, 1)
	dir := tb.TempDir()
	worker := filepath.Join(dir, "worker.go")
	if err := os.WriteFile(worker, []byte(source), 0600); err != nil {
		tb.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/sprint279/worker_call_args_test.go.txt")
	if err != nil {
		tb.Fatal(err)
	}
	tests := filepath.Join(dir, "worker_test.go")
	if err := os.WriteFile(tests, fixture, 0600); err != nil {
		tb.Fatal(err)
	}
	files := []string{worker, tests}
	if checkPool {
		fixture, err := os.ReadFile("testdata/sprint279/worker_call_args_pool_test.go.txt")
		if err != nil {
			tb.Fatal(err)
		}
		poolTests := filepath.Join(dir, "pool_test.go")
		if err := os.WriteFile(poolTests, fixture, 0600); err != nil {
			tb.Fatal(err)
		}
		files = append(files, poolTests)
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go"), files
}

func TestBashPPS279WorkerCallArgs(t *testing.T) {
	goBin, files := s279CallArgsWorker(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Inherit the caller's managed GOCACHE, GOFLAGS and GOMAXPROCS. The child
	// needs its own -race flag; instrumenting interp does not instrument it.
	args := append([]string{"test", "-race", "-count=3", "-timeout=60s", "-v"}, files...)
	cmd := exec.CommandContext(ctx, goBin, args...)
	out, err := cmd.CombinedOutput()
	t.Logf("generated worker gate:\n%s", out)
	if err != nil {
		t.Fatalf("generated worker gate: %v", err)
	}
}

// Reports measurements from inside dispatch, where the scratch is allocated.
// The existing GoSourceNativeCallHotpath benchmark measures the full transport.
func BenchmarkBashPPS279WorkerDispatch(b *testing.B) {
	goBin, files := s279CallArgsWorker(b, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{"test", "-run=^$", "-bench=^BenchmarkS279Dispatch$", "-benchmem", "-benchtime=" + strconv.Itoa(b.N) + "x"}, files...)
	cmd := exec.CommandContext(ctx, goBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		b.Fatalf("generated worker benchmark: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || !strings.HasPrefix(fields[0], "BenchmarkS279Dispatch-") {
			continue
		}
		for i := 2; i+1 < len(fields); i += 2 {
			n, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(n, fields[i+1])
		}
		return
	}
	b.Fatalf("missing generated benchmark result:\n%s", out)
}
