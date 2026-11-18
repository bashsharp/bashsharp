package interp_test

// Sprint: #279; Story: #757; Story-ID: 609c89bfa598

import (
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// Exercise the same execution path as the performance benchmarks, including
// the interpreter's error representation, rather than only a synthetic error.
func TestGoSourceBenchmarkRejectsFailedExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		fails  bool
	}{
		{"return", `package main; func main() { println("finished") }`, false},
		{"exit-zero", `package main; import "os"; func main() { os.Exit(0) }`, false},
		{"exit-seven", `package main; import "os"; func main() { os.Exit(7) }`, true},
		{"panic", `package main; func main() { panic("benchmark sentinel") }`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runBenchmarkGoSource(t.TempDir(), "benchmark-gate.go", tc.source)
			if (err != nil) != tc.fails {
				t.Fatalf("error = %v; want failure = %v", err, tc.fails)
			}
			if tc.name == "exit-seven" {
				var status interp.ExitStatus
				if !errors.As(err, &status) || status != 7 {
					t.Fatalf("error = %v; want wrapped exit status 7", err)
				}
			}
			if tc.name == "panic" && !strings.Contains(err.Error(), "benchmark sentinel") {
				t.Fatalf("error lost panic diagnostic: %v", err)
			}
		})
	}
}
