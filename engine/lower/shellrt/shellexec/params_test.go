package shellexec_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

// awkward is the argv the bridge has to carry without touching a byte: an
// empty parameter, embedded whitespace and a newline, a leading dash that must
// not be read as a flag, quotes and a backslash, and non-ASCII UTF-8.
var awkward = []string{
	"",
	"two words",
	"tab\there",
	"line\nbreak",
	"-n",
	"--",
	"single'quote",
	`double"quote`,
	`back\slash`,
	"$notexpanded",
	"héllo wörld",
	"日本語",
	"emoji 🐚",
}

// paramsProbe prints one line per positional parameter with an unambiguous
// frame, so a joined or re-split argv cannot pass as the original one.
const paramsProbe = `printf 'count=%d\n' "$#"; for a in "$@"; do printf '<%s>\n' "$a"; done`

func wantProbe(params []string) string {
	var b strings.Builder
	b.WriteString("count=" + itoa(len(params)) + "\n")
	for _, p := range params {
		b.WriteString("<" + p + ">\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestSeededParamsReachTheShellByteForByte(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithParams(awkward...))
	if got := s.Params(); !slices.Equal(got, awkward) {
		t.Fatalf("session params %q, want %q", got, awkward)
	}
	if err := s.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), wantProbe(awkward); got != want {
		t.Fatalf("shell saw %q, want %q", got, want)
	}
	// Reading them back must not have rewritten them either.
	if got := s.Params(); !slices.Equal(got, awkward) {
		t.Fatalf("projected params %q, want %q", got, awkward)
	}
}

func TestNoParamsIsAnEmptyParameterList(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "count=0\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if got := s.Params(); len(got) != 0 {
		t.Fatalf("params %q, want none", got)
	}
	// An explicit empty list is the same thing, not "leave them alone".
	s2, out2 := newSession(t, shellrt.WithParams("keep"), shellrt.WithParams())
	if err := s2.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := out2.String(), "count=0\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestLaterWithParamsOverridesTheEarlierDefault(t *testing.T) {
	t.Parallel()
	// This is the shape the generated entry relies on: defaults first, then
	// the caller's own options.
	s, out := newSession(t, shellrt.WithParams("process", "argv"), shellrt.WithParams("caller", "argv", ""))
	if got, want := s.Params(), []string{"caller", "argv", ""}; !slices.Equal(got, want) {
		t.Fatalf("params %q, want %q", got, want)
	}
	if err := s.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), wantProbe([]string{"caller", "argv", ""}); got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

// TestShellPositionalWritesRoundTripAcrossRegions is the persistence claim:
// `shift` and `set --` in one region are visible to the typed side, and a
// typed-side SetParams is visible in the next region. The backend is one
// persistent shell, so this must hold across region boundaries.
func TestShellPositionalWritesRoundTripAcrossRegions(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithParams("a", "b c", "d"))
	if err := s.Shell(t.Context(), "shift"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Params(), []string{"b c", "d"}; !slices.Equal(got, want) {
		t.Fatalf("after shift params %q, want %q", got, want)
	}
	if err := s.Shell(t.Context(), `set -- "$@" "tail arg"`); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Params(), []string{"b c", "d", "tail arg"}; !slices.Equal(got, want) {
		t.Fatalf("after set params %q, want %q", got, want)
	}
	// A typed-side write reaches the next region.
	s.SetParams("x y", "", "-z")
	if err := s.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), wantProbe([]string{"x y", "", "-z"}); got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	// Clearing them from the shell projects as an empty list, and the shell
	// state survives into the following region.
	if err := s.Shell(t.Context(), "set --"); err != nil {
		t.Fatal(err)
	}
	if got := s.Params(); len(got) != 0 {
		t.Fatalf("after `set --` params %q, want none", got)
	}
	seen := out.String()
	if err := s.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimPrefix(out.String(), seen), "count=0\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

// TestTaskParamsAreACopyOfTheSource pins the lifetime rule: a task body sees
// the parameters its parent had at launch, and neither side's later writes
// reach the other.
func TestTaskParamsAreACopyOfTheSource(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithParams("parent", "one two"))
	done := make(chan []string, 1)
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		seen := child.Params()
		if err := child.Shell(ctx, "shift; set -- \"$@\" child-added"); err != nil {
			return err
		}
		done <- append(seen, child.Params()...)
		return nil
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	want := []string{"parent", "one two", "one two", "child-added"}
	if !slices.Equal(got, want) {
		t.Fatalf("child params %q, want %q", got, want)
	}
	if got, want := s.Params(), []string{"parent", "one two"}; !slices.Equal(got, want) {
		t.Fatalf("parent params %q, want %q: the child wrote through", got, want)
	}
	seen := out.String()
	if err := s.Shell(t.Context(), paramsProbe); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimPrefix(out.String(), seen), wantProbe([]string{"parent", "one two"}); got != want {
		t.Fatalf("parent shell saw %q, want %q", got, want)
	}
}
