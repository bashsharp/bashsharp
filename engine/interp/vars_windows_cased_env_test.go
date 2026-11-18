package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// S253.0 (Story-ID 8fc80a770a7d): bash's varenv23.sub runs under a parent
// that exports both a=bcde and A=AVAR, and its `echo global1:$a $b` must
// print global1:bcde (varenv.right:291). On Windows the exec boundary folds
// names case-insensitively — os/exec keeps one survivor per folded name, in
// execEnv's random map order — so the parent packs the colliding pairs into
// the BASHY_CASED_ENV bridge and a child shell restores their exact names.

func TestCasedEnvMarkerOnWindows(t *testing.T) {
	env := []string{"A=AVAR", "PATH=x", "a=bcde"}
	var marker string
	for _, kv := range nativeExecEnvMode(env, true) {
		if v, ok := strings.CutPrefix(kv, bashyCasedEnv+"="); ok {
			marker = v
		}
	}
	if marker == "" {
		t.Fatalf("no %s bridge for a colliding environment", bashyCasedEnv)
	}
	for _, want := range []string{`"A=AVAR"`, `"a=bcde"`} {
		if !strings.Contains(marker, want) {
			t.Errorf("bridge %q does not carry %s", marker, want)
		}
	}
	if strings.Contains(marker, "PATH") {
		t.Errorf("bridge %q carries the uncollided PATH", marker)
	}

	// No collision, no bridge: the common case crosses untouched.
	for _, kv := range nativeExecEnvMode([]string{"A=AVAR", "PATH=x"}, true) {
		if strings.HasPrefix(kv, bashyCasedEnv+"=") {
			t.Errorf("bridge added without a collision: %q", kv)
		}
	}
}

func TestCasedEnvRestoredByChildShell(t *testing.T) {
	// The child's view after Windows folded the block: one survivor — the
	// wrong one, as in run 35758543516 — plus the bridge.
	marker := casedEnvMarker([]string{"a=bcde", "A=AVAR"})
	if marker == "" {
		t.Fatal("casedEnvMarker found no collision")
	}
	env := expand.ListEnviron("A=AVAR", bashyCasedEnv+"="+marker)
	var buf strings.Builder
	r, err := New(Env(env), StdIO(nil, &buf, &buf))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(
		"echo global1:$a $b\necho A:$A\necho bridge:${BASHY_CASED_ENV-unset}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "global1:bcde\nA:AVAR\nbridge:unset\n"
	if got != want {
		t.Fatalf("child shell printed %q, want %q", got, want)
	}
}
