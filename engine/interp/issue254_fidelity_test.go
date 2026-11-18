package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestIssue254ArithProbeStatus(t *testing.T) {
	t.Parallel()

	src := "[[ 12345678909 = $(( 1 << 30 )) ]]\n" +
		"echo eq=$?\n" +
		"[[ 12345678909 = 12345678909 ]]\n" +
		"echo eq=$?\n" +
		"[ 12345678909 -gt $(( 1 << 30 )) ]\n" +
		"echo greater=$?\n" +
		"[[ 12345678909 -gt $(( 1 << 30 )) ]]\n" +
		"echo greater=$?\n" +
		"[[ 12345678909 -ge $(( 1 << 30 )) ]]\n" +
		"echo ge=$?\n" +
		"[[ 12345678909 -ge 12345678909 ]]\n" +
		"echo ge=$?\n" +
		"[[ 12345678909 -le $(( 1 << 30 )) ]]\n" +
		"echo le=$?\n" +
		"[[ 12345678909 -le 12345678909 ]]\n" +
		"echo le=$?\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader([]byte(src)), "./s")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r, err := New(
		StdIO(nil, &buf, &buf),
		Env(expand.ListEnviron()),
		Interactive(false),
		CommandString(false),
		StandardInput(false),
		WithBashCompatErrors(true),
		WithBashSource([]byte(src)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Run returned error after final echo: %v\noutput:\n%s", err, buf.String())
	}
	want := "eq=1\neq=0\ngreater=0\ngreater=0\nge=0\nge=0\nle=1\nle=0\n"
	if got := buf.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestIssue254ArithShiftFalseTestDoesNotLatchExit(t *testing.T) {
	t.Parallel()

	src := "y=$(( 1 << 1 ))\n" +
		"[[ 1 -le 0 ]]\n" +
		"true\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader([]byte(src)), "./s")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(
		Env(expand.ListEnviron()),
		WithBashCompatErrors(true),
		WithBashSource([]byte(src)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("Run returned error after final true: %v", err)
	}
}

func TestIssue254FunctionDiagnosticUsesDefinitionSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tmpSrc := "# line 1\n" +
		"g=\"global\"\n" +
		"local L=\"local\"\n" +
		"test_func() {\n" +
		"  echo \"g = $g\"\n" +
		"  :\n" +
		"  echo \"L = $L\"\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "tmp.sh"), []byte(tmpSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "set -u\n" +
		"main() {\n" +
		"  . ./tmp.sh\n" +
		"}\n" +
		"main\n" +
		"test_func\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader([]byte(src)), "./s")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r, err := New(
		Dir(dir),
		StdIO(nil, &buf, &buf),
		WithBashCompatErrors(true),
		WithBashSource([]byte(src)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		buf.WriteString(err.Error())
	}
	want := "g = global\n./tmp.sh: line 7: L: unbound variable\nexit status 1"
	if got := buf.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}
