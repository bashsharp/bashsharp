//go:build full

package lower_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCPPWorkerStreamStateLowered(t *testing.T) {
	template, err := os.ReadFile("../internal/testdata/cpp-stream-state/stream.cpp")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, action, result, value, diagnostic string }{
		{"empty", `std::istringstream input(""); std::cout << input.rdbuf();`, "return 7;", "7", ""},
		{"missing", `std::ifstream input("/nonexistent-bashpp-stream-state/input"); std::cout << input.rdbuf();`, "return 7;", "7", ""},
		{"exception_mask", `try { std::cout.exceptions(std::ios::failbit); std::cout.setstate(std::ios::failbit); } catch(const std::ios_base::failure&) {}`, "return 7;", "7", ""},
		{"error_frame", `std::cout.setstate(std::ios::failbit);`, `throw std::runtime_error("stream-domain-error");`, "0", "bash++: foreign call c.example_main failed: stream-domain-error\n"},
		{"native_exit", `std::cout.setstate(std::ios::failbit);`, "std::exit(23);", "0", "bash++: foreign call c.example_main failed: C++ worker failed: exit status 23\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.ReplaceAll(string(template), "// STREAM_ACTION", tc.action)
			source = strings.ReplaceAll(source, "// RETURN_ACTION", tc.result)
			interpreted, compiled := foreignOutcomes(t, "~~~cpp as c\n"+source+"~~~\nvalue, err := c.example_main()\necho \"$value|$err\"\n")
			for mode, got := range map[string]foreignOutcome{"interpreted": interpreted, "compiled": compiled} {
				if got.stdout != "body\ncallback\n"+tc.value+"|\n" || got.stderr != tc.diagnostic {
					t.Fatalf("%s stdout=%q stderr=%q", mode, got.stdout, got.stderr)
				}
			}
		})
	}
}

func TestCPPWorkerIfstreamCorpusLowered(t *testing.T) {
	for _, name := range []string{"L38", "L144", "L49"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile("../internal/testdata/cpp-stream-state/" + name + ".bsh")
			if err != nil {
				t.Fatal(err)
			}
			got := testPythonFenceInterpretedNativeParityAt(t, string(source), filepath.Join(t.TempDir(), name+".bsh"))
			if got != "" {
				t.Fatalf("stdout=%q", got)
			}
		})
	}
}
