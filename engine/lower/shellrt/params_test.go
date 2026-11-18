package shellrt_test

import (
	"slices"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestStateCloneDoesNotAliasParams(t *testing.T) {
	t.Parallel()
	src := shellrt.State{Params: []string{"a", "b c", ""}}
	clone := src.Clone()
	if !slices.Equal(clone.Params, src.Params) {
		t.Fatalf("clone params %q, want %q", clone.Params, src.Params)
	}
	clone.Params[0] = "written"
	clone.Params = append(clone.Params, "extra")
	if got, want := src.Params, []string{"a", "b c", ""}; !slices.Equal(got, want) {
		t.Fatalf("source params %q, want %q: the clone aliased the slice", got, want)
	}
	// The reverse direction too: a source write must not reach the clone.
	src.Params[1] = "mutated"
	if got, want := clone.Params, []string{"written", "b c", "", "extra"}; !slices.Equal(got, want) {
		t.Fatalf("clone params %q, want %q", got, want)
	}
	// A nil parameter list clones as a nil one, not as a shared empty slice.
	if got := (shellrt.State{}).Clone().Params; len(got) != 0 {
		t.Fatalf("empty clone params %q, want none", got)
	}
}

func TestWithParamsCopiesTheCallerSlice(t *testing.T) {
	t.Parallel()
	argv := []string{"one", "two"}
	s, err := shellrt.NewSession(shellrt.WithParams(argv...))
	if err != nil {
		t.Fatal(err)
	}
	argv[0] = "clobbered"
	if got, want := s.Params(), []string{"one", "two"}; !slices.Equal(got, want) {
		t.Fatalf("params %q, want %q: the option aliased the caller's slice", got, want)
	}
	// The accessor hands out a copy as well.
	got := s.Params()
	got[0] = "written"
	if now, want := s.Params(), []string{"one", "two"}; !slices.Equal(now, want) {
		t.Fatalf("params %q, want %q: the accessor aliased the session", now, want)
	}
}

// TestParamsProjectWithoutAShell keeps the typed-only artifact honest: a
// session with no backend still carries positional parameters, because they
// are State, not shell-only state.
func TestParamsProjectWithoutAShell(t *testing.T) {
	t.Parallel()
	s, err := shellrt.NewSession(shellrt.WithParams("x", "", "y z"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x", "", "y z"}
	if got := s.Snapshot().Params; !slices.Equal(got, want) {
		t.Fatalf("snapshot params %q, want %q", got, want)
	}
	snapshot := s.Snapshot()
	snapshot.Params[0] = "written"
	if got := s.Params(); !slices.Equal(got, want) {
		t.Fatalf("params %q, want %q: the snapshot aliased the session", got, want)
	}
	s.SetParams()
	if got := s.Params(); len(got) != 0 {
		t.Fatalf("params %q, want none", got)
	}
}
