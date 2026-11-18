package shellrt

import (
	"errors"
	"testing"
)

// TestCheckBuiltinNamesOwnerAndBuiltin pins the interpreter's builtin wording:
// the diagnostic names the marked owner and the builtin, never the alias the
// call reached the container through and never a path.
func TestCheckBuiltinNamesOwnerAndBuiltin(t *testing.T) {
	var state ReadonlyState
	root := map[string]int{"a": 1}
	alias := root
	if err := state.Mark("m", &root); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		binding, target  any
		builtin, message string
	}{
		{"root", &root, root, "delete", `BASHPP-EREADONLY-MUTATION: cannot mutate readonly value "m" through delete`},
		{"alias", &alias, alias, "clear", `BASHPP-EREADONLY-MUTATION: cannot mutate readonly value "m" through clear`},
	} {
		err := state.CheckBuiltin(tc.binding, tc.target, tc.builtin)
		if err == nil || err.Error() != tc.message {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var readonly *ReadonlyError
		if !errors.As(err, &readonly) || readonly.ExitStatus() != 2 {
			t.Fatalf("%s: %T %v", tc.name, err, err)
		}
	}
	other := map[string]int{"a": 1}
	if err := state.CheckBuiltin(&other, other, "delete"); err != nil {
		t.Fatal("independent map reported readonly", err)
	}
}

// TestCheckBuiltinFollowsSliceRegions covers the append/copy/clear target that
// is a view of a marked slice: the recorded region, not the binding, is what
// makes it readonly.
func TestCheckBuiltinFollowsSliceRegions(t *testing.T) {
	var state ReadonlyState
	numbers := []int{1, 2, 3}
	if err := state.Mark("s", &numbers); err != nil {
		t.Fatal(err)
	}
	view := numbers[1:]
	if err := state.CheckBuiltin(&view, view, "append"); err == nil {
		t.Fatal("overlapping view escaped the builtin guard")
	}
	fresh := []int{1, 2, 3}
	if err := state.CheckBuiltin(&fresh, fresh, "append"); err != nil {
		t.Fatal("independent slice reported readonly", err)
	}
}

// TestMustReadonlyUnwindsTypedError pins the boundary contract: a guard failure
// unwinds with its own typed error so deferred code runs and the program
// boundary can report it once and take its exit status. Nothing is recorded in
// the runtime, so two programs cannot see each other's failures.
func TestMustReadonlyUnwindsTypedError(t *testing.T) {
	var state ReadonlyState
	value := 1
	if err := state.Mark("value", &value); err != nil {
		t.Fatal(err)
	}
	deferred := false
	status, err := func() (status int, err error) {
		defer func() {
			recovered, ok := recover().(error)
			if !ok {
				return
			}
			err = recovered
			var exit interface{ ExitStatus() int }
			if errors.As(recovered, &exit) {
				status = exit.ExitStatus()
			}
		}()
		defer func() { deferred = true }()
		MustReadonly(state.CheckAssign(&value))
		return 0, nil
	}()
	if !deferred {
		t.Fatal("the unwind skipped deferred code")
	}
	if status != 2 || err == nil || err.Error() != `BASHPP-EREADONLY-MUTATION: cannot assign to readonly value "value"` {
		t.Fatalf("status=%d err=%v", status, err)
	}
	MustReadonly(nil) // a passing guard must not unwind
}
