//go:build full

package interp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// sprint165FramesRun runs one Go source reproducer of the frames-1 lane
// through the interpreter and returns its combined output and run error.
func sprint165FramesRun(t *testing.T, mechanism, name string) (string, error) {
	t.Helper()
	root := filepath.Join("testdata", "sprint165", "frames-1", mechanism)
	source, err := os.ReadFile(filepath.Join(root, name+".go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("%s: parse: %v", name, err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), program.File)
	return out.String(), err
}

// sprint165FramesExpect runs a reproducer and compares its output with the
// sibling .expected file byte for byte.
func sprint165FramesExpect(t *testing.T, mechanism, name string, wantErr bool) {
	t.Helper()
	got, err := sprint165FramesRun(t, mechanism, name)
	if (err != nil) != wantErr {
		t.Fatalf("%s/%s: run error %v, want error %v; output=%q", mechanism, name, err, wantErr, got)
	}
	want, readErr := os.ReadFile(filepath.Join("testdata", "sprint165", "frames-1", mechanism, name+".expected"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got != string(want) {
		t.Fatalf("%s/%s: output\n%s\nwant\n%s", mechanism, name, got, string(want))
	}
}

// sprint165FramesRunNamed runs a reproducer parsed under the given source
// name: the name a line directive's relative filename resolves against.
func sprint165FramesRunNamed(t *testing.T, mechanism, name, sourceName string) (string, error) {
	t.Helper()
	root := filepath.Join("testdata", "sprint165", "frames-1", mechanism)
	source, err := os.ReadFile(filepath.Join(root, name+".go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), sourceName, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("%s: parse: %v", name, err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), program.File)
	return out.String(), err
}

// The frame table reports the file a line directive puts in effect at the
// frame's position: absolute as written, cleared as "??", relative as written
// when the source was named without a directory and resolved against that
// directory otherwise. Frames no directive governs name the physical file.
func TestBashPPSprint165FramesLineDirectives(t *testing.T) {
	sprint165FramesExpect(t, "linedir", "line_directive_frames", false)
	abs, err := filepath.Abs(filepath.Join("testdata", "sprint165", "frames-1", "linedir", "line_directive_frames.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, runErr := sprint165FramesRunNamed(t, "linedir", "line_directive_frames", abs)
	if runErr != nil {
		t.Fatalf("absolute name: %v; output=%q", runErr, got)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "sprint165", "frames-1", "linedir", "line_directive_frames.absolute.expected"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("absolute name: output\n%s\nwant\n%s", got, string(want))
	}
}

// runtime.Caller counts runtime.main and runtime.goexit below main.main;
// runtime.Callers lists itself first and fills counters that resolve at pc-1;
// runtime.CallersFrames replays them as runtime.Frame values with a working
// Func; function literals carry Go's names; a deferred call for a panic is
// called by runtime.gopanic, printed as panic({...}) in tracebacks, and
// debug.Stack names itself first. A skip past the last frame reports
// nothing, and a counter no function owns resolves to no function.
func TestBashPPSprint165FramesCallersWalk(t *testing.T) {
	sprint165FramesExpect(t, "callers", "callers_walk", false)
}

// A deferred call finds the deferring frame at its return point — the
// return statement's line, also when its expression called a function, or
// the closing brace of a body that ran to its end. An ordinary callee still
// finds its caller at the call's line, and a panicking frame stays at the
// fault line.
func TestBashPPSprint165FramesDeferSite(t *testing.T) {
	sprint165FramesExpect(t, "defersite", "defer_site", false)
}

// Frames are named as Go names them: declared functions and methods by
// their qualified names, generic ones with `[...]`, function literals after
// the declaration they are written in and numbered in source order, nested
// literals with a dotted index, package-level literals after init.
func TestBashPPSprint165FramesLiteralNames(t *testing.T) {
	sprint165FramesExpect(t, "names", "literal_names", false)
}
