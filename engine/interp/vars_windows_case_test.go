package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// Shell variable names are case-sensitive on every platform, Windows
// included: bash's varenv23.sub exports A=AVAR and expects the child's
// unset `$a` to stay unset. Only the OS environment is case-insensitive,
// and expand.ListEnviron handles that at import.
func TestOverlayEnvironNamesAreCaseSensitive(t *testing.T) {
	base := expand.ListEnviron("A=AVAR", "PATH=x")
	o := &overlayEnviron{parent: base, values: map[string]namedVariable{}}
	for _, name := range []string{"a", "A", "path", "PATH", "Foo"} {
		if got := o.normalize(name); got != name {
			t.Errorf("normalize(%q) = %q, want it unchanged", name, got)
		}
	}
	if vr := o.Get("a"); vr.IsSet() {
		t.Errorf(`$a resolved to %q; an exported A must not answer it`, vr.String())
	}
	if vr := o.Get("A"); vr.String() != "AVAR" {
		t.Errorf(`$A = %q, want AVAR`, vr.String())
	}
}
