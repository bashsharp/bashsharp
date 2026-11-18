package polyglot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCWorkerAtexitCorpus(t *testing.T) {
	script, err := os.ReadFile("../internal/testdata/c-atexit/L53.bsh")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../internal/testdata/c-atexit/L53.expected")
	if err != nil {
		t.Fatal(err)
	}
	want := "This is executed first.\nThis is executed next.\n"
	if string(golden) != "# rc=0\n"+want || len(want) != 47 {
		t.Fatalf("golden=%q", golden)
	}
	source := strings.Split(strings.Split(string(script), "~~~c as c\n")[1], "~~~")[0]
	testCWorkerLifecycle(t, source, want, "", 0, "", 0)
}

func TestCWorkerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, body, callback, out, diagnostic string
		status                                int
		failure                               string
	}{
		{"return", "return 7;", "", "body\nlast\t\x00tail", "stderr\n", 0, ""},
		{"exit_zero", "exit(0);", "", "body\nlast\t\x00tail", "stderr\n", 0, "no result frame"},
		{"exit_nonzero", "exit(23);", "", "body\nlast\t\x00tail", "stderr\n", 23, "worker failed"},
		{"callback_exit", "return 7;", "exit(29);", "body\nlast\t\x00tail", "stderr\n", 29, "worker failed"},
		{"immediate_exit", "fflush(stdout); _Exit(31);", "", "body\n", "", 31, "worker failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "#include <stdio.h>\n#include <stdlib.h>\nstatic void done(void){fwrite(\"last\\t\\0tail\",1,10,stdout); fputs(\"stderr\\n\",stderr);" + tc.callback + "}\nint example_main(void){atexit(done); puts(\"body\");" + tc.body + "}\n"
			testCWorkerLifecycle(t, source, tc.out, tc.diagnostic, tc.status, tc.failure, 7)
		})
	}
}

func testCWorkerLifecycle(t *testing.T, source, wantOut, wantErr string, status int, failure string, wantValue int64) {
	t.Helper()
	testNativeWorkerLifecycle(t, "c", source, wantOut, wantErr, status, failure, wantValue)
}

func testNativeWorkerLifecycle(t *testing.T, language, source, wantOut, wantErr string, status int, failure string, wantValue int64) {
	t.Helper()
	compiler, standard := "clang", "-std=c17"
	if language == "cpp" {
		compiler, standard = "clang++", "-std=c++17"
	}
	clang, err := exec.LookPath(compiler)
	if err != nil {
		t.Fatal("real clang required:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	src := filepath.Join(dir, "oracle."+language)
	bin := filepath.Join(dir, "oracle")
	// A fence function's integer return is a value, not a process exit status.
	// The native driver deliberately discards it, as the one-shot worker does.
	if err := os.WriteFile(src, []byte(source+"\nint main(void){example_main(); return 0;}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, clang, standard, src, "-o", bin).CombinedOutput(); err != nil {
		t.Fatalf("native build: %v %s", err, out)
	}
	cmd := exec.CommandContext(ctx, bin)
	var out, diagnostic bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	nativeErr := cmd.Run()
	nativeStatus := 0
	if nativeErr != nil {
		var exit *exec.ExitError
		if !errors.As(nativeErr, &exit) {
			t.Fatal(nativeErr)
		}
		nativeStatus = exit.ExitCode()
	}
	if nativeStatus != status || out.String() != wantOut || diagnostic.String() != wantErr {
		t.Fatalf("native status=%d stdout=%q stderr=%q", nativeStatus, out.String(), diagnostic.String())
	}
	var runtime interface {
		Analyzer
		Runtime
	} = C{Command: clang}
	if language == "cpp" {
		runtime = CPP{Command: clang}
	}
	plans, err := Prepare(ctx, []Block{{Language: language, Source: source}}, map[string]Analyzer{language: runtime})
	if err != nil {
		t.Fatal(err)
	}
	m := Start(plans[0], runtime)
	defer m.Close()
	for i := 0; i < 2; i++ { // each call owns a new native process and callback list
		got, err := m.Call(ctx, "example_main")
		if got.Stdout != wantOut || got.Stderr != wantErr {
			t.Fatalf("call=%#v err=%v", got, err)
		}
		if failure == "" {
			if err != nil {
				t.Fatal(err)
			}
			if got.Value != wantValue {
				t.Fatalf("value=%v", got.Value)
			}
		} else if err == nil || !strings.Contains(err.Error(), failure) {
			t.Fatalf("error=%v want %s", err, failure)
		}
		if status != 0 {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != status {
				t.Fatalf("native status lost: %v", err)
			}
		}
	}
}

func TestCPPWorkerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, body, failure string
		status              int
	}{
		{"return", "return 7;", "", 0},
		{"exit_zero", "exit(0);", "no result frame", 0},
		{"exit_nonzero", "exit(23);", "worker failed", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "#include <cstdio>\n#include <cstdlib>\nstruct Cleanup { ~Cleanup(){fputs(\"destructor\\n\",stdout); fputs(\"stderr\\n\",stderr);} };\nstatic void done(){fputs(\"callback\\n\",stdout);}\nint example_main(){static Cleanup cleanup; atexit(done); puts(\"body\");" + tc.body + "}\n"
			testNativeWorkerLifecycle(t, "cpp", source, "body\ncallback\ndestructor\n", "stderr\n", tc.status, tc.failure, 7)
		})
	}
}

func TestNativeWorkerResultFrames(t *testing.T) {
	const frame = "\x1eBASHPP\tOK\tint\t37\n"
	for _, language := range []string{"C", "C++"} {
		for _, tc := range []struct{ name, data, want, failure string }{
			{"trailing", "before" + frame + "after\t\x00", "beforeafter\t\x00", ""},
			{"crlf", strings.TrimSuffix(frame, "\n") + "\r\nafter", "after", ""},
			{"multiple", frame + frame, frame + frame, "multiple"},
			{"callback_marker", frame + "\x1eBASHPP\tnot a frame", frame + "\x1eBASHPP\tnot a frame", "multiple"},
			{"unterminated", strings.TrimSuffix(frame, "\n"), strings.TrimSuffix(frame, "\n"), "invalid"},
			{"bad_hex", "\x1eBASHPP\tOK\tint\tzz\nafter", "after", "payload"},
			{"bad_fields", "\x1eBASHPP\tOK\n", "", "frame"},
			{"bad_status", "\x1eBASHPP\tWHAT\tint\t37\n", "", "status"},
		} {
			t.Run(language+"/"+tc.name, func(t *testing.T) {
				got, err := decodeNativeResult([]byte(tc.data), "stderr", nil, language)
				if got.Stdout != tc.want || got.Stderr != "stderr" {
					t.Fatalf("result=%#v", got)
				}
				if tc.failure == "" {
					if err != nil || got.Value != int64(7) {
						t.Fatalf("result=%#v err=%v", got, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("err=%v", err)
				}
			})
		}
	}
}
