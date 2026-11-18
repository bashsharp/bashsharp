package polyglot

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCPPWorkerStreamState(t *testing.T) {
	template, err := os.ReadFile("../internal/testdata/cpp-stream-state/stream.cpp")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, action, result, failure string
		status                        int
	}{
		{"empty", `std::istringstream input(""); std::cout << input.rdbuf();`, "return 7;", "", 0},
		{"missing", `std::ifstream input("/nonexistent-bashpp-stream-state/input"); std::cout << input.rdbuf();`, "return 7;", "", 0},
		{"badbit", `std::cout.setstate(std::ios::badbit);`, "return 7;", "", 0},
		{"exception_mask", `try { std::cout.exceptions(std::ios::failbit); std::cout.setstate(std::ios::failbit); } catch(const std::ios_base::failure&) {}`, "return 7;", "", 0},
		{"error_frame", `std::cout.setstate(std::ios::failbit);`, `throw std::runtime_error("stream-domain-error");`, "stream-domain-error", 0},
		{"native_exit", `std::cout.setstate(std::ios::failbit);`, "std::exit(23);", "worker failed", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.ReplaceAll(string(template), "// STREAM_ACTION", tc.action)
			source = strings.ReplaceAll(source, "// RETURN_ACTION", tc.result)
			if tc.name == "error_frame" {
				// The standalone native driver catches the domain exception, just as the
				// worker does; its transport representation is checked through Call below.
				source = strings.Replace(source, "int example_main()", "int throwing_main()", 1)
				source += "\nint example_main(){try{return throwing_main();}catch(const std::exception&){return 7;}}\n"
				testNativeWorkerLifecycle(t, "cpp", source, "body\ncallback\n", "", 0, "", 7)
				source = strings.Replace(source, "catch(const std::exception&){return 7;}", "catch(const std::exception&){throw;}", 1)
				testCPPStreamError(t, source)
			} else {
				testNativeWorkerLifecycle(t, "cpp", source, "body\ncallback\n", "", tc.status, tc.failure, 7)
			}
		})
	}
}

func testCPPStreamError(t *testing.T, source string) {
	t.Helper()
	compiler, err := exec.LookPath("clang++")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime := CPP{Command: compiler}
	plans, err := Prepare(ctx, []Block{{Language: "cpp", Source: source}}, map[string]Analyzer{"cpp": runtime})
	if err != nil {
		t.Fatal(err)
	}
	m := Start(plans[0], runtime)
	defer m.Close()
	got, err := m.Call(ctx, "example_main")
	if err == nil || !strings.Contains(err.Error(), "stream-domain-error") || got.Stdout != "body\ncallback\n" || got.Stderr != "" {
		t.Fatalf("call=%#v err=%v", got, err)
	}
}

func TestCPPWorkerIfstreamCorpus(t *testing.T) {
	for _, name := range []string{"L38", "L144", "L49"} {
		t.Run(name, func(t *testing.T) {
			script, err := os.ReadFile("../internal/testdata/cpp-stream-state/" + name + ".bsh")
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile("../internal/testdata/cpp-stream-state/" + name + ".expected")
			if err != nil {
				t.Fatal(err)
			}
			if string(golden) != "# rc=0\n" {
				t.Fatalf("golden=%q", golden)
			}
			source := strings.Split(strings.Split(string(script), "~~~cpp as cxx\n")[1], "~~~")[0]
			t.Chdir(t.TempDir()) // preserve the missing-file condition in the original source
			testNativeWorkerLifecycle(t, "cpp", source, "", "", 0, "", 0)
		})
	}
}
