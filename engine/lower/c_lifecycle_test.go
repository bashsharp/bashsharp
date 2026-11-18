//go:build full

package lower_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCWorkerAtexitLowered(t *testing.T) {
	source, err := os.ReadFile("../internal/testdata/c-atexit/L53.bsh")
	if err != nil {
		t.Fatal(err)
	}
	got := testPythonFenceInterpretedNativeParityAt(t, string(source), filepath.Join(t.TempDir(), "atexit.bsh"))
	if got != "This is executed first.\nThis is executed next.\n" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestCWorkerLifecycleLowered(t *testing.T) {
	for _, tc := range []struct{ name, body, callback, failure string }{
		{"return", "return 7;", "", ""},
		{"exit_zero", "exit(0);", "", "C worker returned no result frame"},
		{"exit_nonzero", "exit(23);", "", "C worker failed: exit status 23"},
		{"callback_exit", "return 7;", "exit(29);", "C worker failed: exit status 29"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "~~~c as c\n#include <stdio.h>\n#include <stdlib.h>\nstatic void done(void){puts(\"after\"); fputs(\"diagnostic\\n\",stderr);" + tc.callback + "}\nint example_main(void){atexit(done); puts(\"before\");" + tc.body + "}\n~~~\nvalue, err := c.example_main()\necho \"$value|$err\"\n"
			interpreted, compiled := foreignOutcomes(t, source)
			want := "before\nafter\n7|\n"
			wantErr := "diagnostic\n"
			if tc.failure != "" {
				want = "before\nafter\n0|\n"
				wantErr += "bash++: foreign call c.example_main failed: " + tc.failure + "\n"
			}
			for mode, got := range map[string]foreignOutcome{"interpreted": interpreted, "compiled": compiled} {
				if got.stdout != want || got.stderr != wantErr {
					t.Fatalf("%s stdout=%q stderr=%q want=%q", mode, got.stdout, got.stderr, want)
				}
			}
		})
	}
}

func TestCPPWorkerLifecycleLowered(t *testing.T) {
	source := "~~~cpp as c\n#include <cstdio>\n#include <cstdlib>\nstruct Cleanup { ~Cleanup(){puts(\"destructor\"); fputs(\"diagnostic\\n\",stderr);} };\nstatic void done(){puts(\"callback\");}\nint example_main(){static Cleanup cleanup; atexit(done); puts(\"body\"); return 7;}\n~~~\nvalue, err := c.example_main()\necho \"$value|$err\"\n"
	interpreted, compiled := foreignOutcomes(t, source)
	for mode, got := range map[string]foreignOutcome{"interpreted": interpreted, "compiled": compiled} {
		if got.stdout != "body\ncallback\ndestructor\n7|\n" || got.stderr != "diagnostic\n" {
			t.Fatalf("%s stdout=%q stderr=%q", mode, got.stdout, got.stderr)
		}
	}
}
