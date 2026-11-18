//go:build full

package interp_test

// Sprint: #118; Story: #64; Story-ID: 45be321bddfb
//
// Unchanged Go-by-Example sources that exercise the callback, signature and
// copied-slice bridge, replayed by the interpreter and compared against a real
// Go build of the same bytes. The archived fixtures are shared with the
// compiled-mode replay in ../lower and their hashes are checked before use, so
// neither replay can quietly edit the corpus it measures.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const goSourceGbEDir = "../lower/testdata/gosource-gbe"

// goSourceGbEFixture reads one archived original source and authenticates it
// against the pinned digest the compiled replay uses.
func goSourceGbEFixture(t *testing.T, name string) string {
	t.Helper()
	pinsBytes, err := os.ReadFile(filepath.Join(goSourceGbEDir, "sha256.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err := json.Unmarshal(pinsBytes, &pins); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(goSourceGbEDir, name+".go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != pins[name] {
		t.Fatalf("original %s fixture bytes changed: %s", name, got)
	}
	return string(data)
}

// TestGoSourceGbESortingByFunctions replays the unchanged sorting-by-functions
// example. It needs the copied-slice bridge (an original slice crossing with an
// original comparison callback), value-semantics struct callback parameters,
// and interpreter-side slices.SortFunc/cmp.Compare.
func TestGoSourceGbESortingByFunctions(t *testing.T) {
	differGoSource(t, goSourceGbEFixture(t, "sorting-by-functions"), nil, "")
}

// TestGoSourceGbEXML replays the unchanged xml example: a local type with a
// value-receiver String method over slice storage, structural marshalling, and
// a native decoder writing through a pointer to an original variable.
func TestGoSourceGbEXML(t *testing.T) {
	differGoSource(t, goSourceGbEFixture(t, "xml"), nil, "")
}

// goSourceGbEServer starts one original server program on a free port both
// natively and interpreted, drives it with the same client requests, and
// returns each side's client-visible answers and program output.
type goSourceServerRun struct {
	answers []string
	stdout  string
}

func goSourceFreePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// goSourceAwaitServer waits for the program under test to accept connections.
func goSourceAwaitServer(t *testing.T, port string, stop <-chan struct{}) bool {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		select {
		case <-stop:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// runNativeServer builds and runs the original source as a real Go program.
func runNativeServer(t *testing.T, source, port string, drive func(*testing.T, string) []string) goSourceServerRun {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "oracle")
	if out, err := exec.Command("go", "build", "-p", "2", "-o", binary, path).CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	var stdout bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() { cmd.Wait(); close(stop) }()
	if !goSourceAwaitServer(t, port, stop) {
		t.Fatalf("native server never accepted: %s", stdout.String())
	}
	answers := drive(t, port)
	_ = cmd.Process.Kill()
	<-stop
	return goSourceServerRun{answers: answers, stdout: stdout.String()}
}

// runInterpretedServer runs the same unchanged bytes on the interpreter.
func runInterpretedServer(t *testing.T, source, port string, drive func(*testing.T, string) []string) goSourceServerRun {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stop := make(chan struct{})
	go func() { runner.Run(ctx, program.File); close(stop) }()
	if !goSourceAwaitServer(t, port, stop) {
		t.Fatalf("interpreted server never accepted: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	answers := drive(t, port)
	cancel()
	<-stop
	if stderr.Len() > 0 {
		t.Fatalf("interpreted server wrote to stderr: %q", stderr.String())
	}
	return goSourceServerRun{answers: answers, stdout: stdout.String()}
}

// TestGoSourceGbEHTTPServer replays the unchanged http-server example. Both
// registered handlers are original functions the dependency retains and calls
// back with its own http.ResponseWriter and *http.Request, and the headers
// handler ranges over a dependency-owned map of dependency-owned slices.
func TestGoSourceGbEHTTPServer(t *testing.T) {
	fixture := goSourceGbEFixture(t, "http-server")
	drive := func(t *testing.T, port string) []string {
		t.Helper()
		var answers []string
		for _, route := range []string{"/hello", "/headers"} {
			resp, err := http.Get("http://127.0.0.1:" + port + route)
			if err != nil {
				t.Fatalf("GET %s: %v", route, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			// The headers handler ranges a Go map, whose order is unspecified
			// on both sides; the set of echoed headers is what it reports.
			lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
			sort.Strings(lines)
			answers = append(answers, fmt.Sprintf("%s %d %s", route, resp.StatusCode, strings.Join(lines, "|")))
		}
		return answers
	}
	port := goSourceFreePort(t)
	source := strings.Replace(fixture, ":8090", ":"+port, 1)
	if source == fixture {
		t.Fatal("fixture no longer binds the documented port")
	}
	want := runNativeServer(t, source, port, drive)
	got := runInterpretedServer(t, source, port, drive)
	if fmt.Sprint(got.answers) != fmt.Sprint(want.answers) || got.stdout != want.stdout {
		t.Fatalf("interpreter=%+v native=%+v", got, want)
	}
}

// TestGoSourceGbEContext replays the unchanged context example. The original
// handler selects between a dependency-owned timer channel and the request
// context's Done channel, so aborting the client must reach the interpreted
// select and run the handler's deferred report.
func TestGoSourceGbEContext(t *testing.T) {
	fixture := goSourceGbEFixture(t, "context")
	drive := func(t *testing.T, port string) []string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/hello", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatal("the client was supposed to abort before the 10s reply")
		}
		// Let the abandoned handler finish reporting before the program stops.
		time.Sleep(2 * time.Second)
		return []string{"aborted"}
	}
	port := goSourceFreePort(t)
	source := strings.Replace(fixture, ":8090", ":"+port, 1)
	if source == fixture {
		t.Fatal("fixture no longer binds the documented port")
	}
	want := runNativeServer(t, source, port, drive)
	got := runInterpretedServer(t, source, port, drive)
	const documented = "server: hello handler started\nserver: context canceled\nserver: hello handler ended\n"
	if want.stdout != documented {
		t.Fatalf("native oracle output changed: %q", want.stdout)
	}
	if got.stdout != want.stdout {
		t.Fatalf("interpreter=%q native=%q", got.stdout, want.stdout)
	}
}

// TestGoSourceGbETestingAndBenchmarking replays the unchanged
// testing-and-benchmarking example through the hosted driver: it is a test
// package, not a program, so it has no main. Both Test roots, every dynamic
// subtest and the Benchmark run, driven by the real testing scheduler.
func TestGoSourceGbETestingAndBenchmarking(t *testing.T) {
	source := goSourceGbEFixture(t, "testing-and-benchmarking")
	dir := t.TempDir()
	path := filepath.Join(dir, "main_test.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: path, Data: []byte(source)}},
		gosource.Options{Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if program.Package != "main" {
		t.Fatalf("the fixture documents package main, got %q", program.Package)
	}
	var out, errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v; %s", err, errs.String())
	}
	defer session.Close()
	tests, err := session.Tests()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, test := range tests {
		names = append(names, test.Name)
	}
	if fmt.Sprint(names) != "[TestIntMinBasic TestIntMinTableDriven]" {
		t.Fatalf("discovered %v", names)
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			if err := session.Run(ctx, test.Name, t); err != nil {
				t.Fatalf("run: %v; %s", err, errs.String())
			}
		})
	}
	benchmarks, err := session.Benchmarks()
	if err != nil {
		t.Fatal(err)
	}
	if len(benchmarks) != 1 || benchmarks[0].Name != "BenchmarkIntMin" {
		t.Fatalf("discovered %+v", benchmarks)
	}
	// The real scheduler owns the iteration count; the interpreter only runs
	// the original body each time b.Loop reports another iteration.
	failure := ""
	result := testing.Benchmark(func(b *testing.B) {
		if err := session.RunBenchmark(ctx, benchmarks[0].Name, b); err != nil {
			failure = fmt.Sprintf("%v; %s", err, errs.String())
			b.FailNow()
		}
	})
	if failure != "" {
		t.Fatal(failure)
	}
	if result.N < 2 {
		t.Fatalf("the benchmark body ran %d times", result.N)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original source changed")
	}
}

// The production corpus gate supplies a separate main with contextual
// imported struct literals. Keep that exact syntax covered as well as the
// hosted LoadGoSourceTests API above.
func TestGoSourceGbETestingDriverComposite(t *testing.T) {
	source := `package main
import (
	"regexp"
	"testing"
)
func TestSample(t *testing.T) { t.Run("child", func(*testing.T) {}) }
func match(pat, str string) (bool, error) { return regexp.MatchString(pat, str) }
func main() {
	testing.Main(match,
		[]testing.InternalTest{{Name: "TestSample", F: TestSample}},
		[]testing.InternalBenchmark{}, nil)
}
`
	differGoSource(t, source, nil, "")
}
