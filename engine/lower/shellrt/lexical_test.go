package shellrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func lexicalProgram(t *testing.T) *Program {
	t.Helper()
	p, err := NewProgram(WithStdio(nil, new(bytes.Buffer), new(bytes.Buffer)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Session.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}

func lexicalValue(t *testing.T, b *LexicalBindings, s *Session, name, want string, present bool) {
	t.Helper()
	got, ok, err := b.ShellValue(s, name)
	if got != want || ok != present || err != nil {
		t.Fatalf("%s=(%q,%v,%v), want (%q,%v)", name, got, ok, err, want, present)
	}
}

func TestLexicalCapturedNamesShareLaterWrites(t *testing.T) {
	p := lexicalProgram(t)
	before := p.Bindings.CaptureNames(nil)
	cell := Cell[int](p.Bindings, "scope0:x", "x", KindScalar)
	lexicalValue(t, p.Bindings, p.Session, "x", "", false)
	cell.Value, cell.Present = 1, true
	names := map[string]string{"x": "scope0:x"}
	after := p.Bindings.CaptureNames(names)
	delete(names, "x") // CaptureNames owns its name map.
	if cell != Cell[int](after, "scope0:x", "x", KindScalar) {
		t.Fatal("cell identity changed")
	}
	lexicalValue(t, before, p.Session, "x", "", false)
	lexicalValue(t, after, p.Session, "x", "1", true)
	exchange, err := after.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	p.Session.SetString("x", "42")
	p.Session.SetString("unrelated", "survives")
	if err := exchange.EndShell(p.Session); err != nil {
		t.Fatal(err)
	}
	if cell.Value != 42 || !cell.Present {
		t.Fatal(cell)
	}
	lexicalValue(t, after, p.Session, "x", "42", true)
	lexicalValue(t, before, p.Session, "x", "", false)
	lexicalValue(t, before, p.Session, "unrelated", "survives", true)
}

func TestLexicalExchangeRestoresOriginalVariablesAndAttributes(t *testing.T) {
	p := lexicalProgram(t)
	original := Var{Kind: Indexed, List: []string{"outer", "second"}, Exported: true, ReadOnly: true}
	p.Session.Set("x", original)
	cell := Cell[string](p.Bindings, "x", "x", KindScalar)
	cell.Value, cell.Present = "inner", true
	exchange, err := p.Bindings.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	projected, _ := p.Session.Get("x")
	if projected.Str != "inner" || !projected.Exported || !projected.ReadOnly {
		t.Fatal(projected)
	}
	// Backend enforcement of readonly belongs to the shell; force a write here
	// only to exercise exchange restoration independently from that backend.
	p.Session.SetString("x", "changed")
	if err := exchange.EndShell(p.Session); err != nil {
		t.Fatal(err)
	}
	if cell.Value != "changed" {
		t.Fatal(cell)
	}
	got, _ := p.Session.Get("x")
	if !got.Equal(original) {
		t.Fatalf("restored %v, want %v", got, original)
	}
	if err := exchange.EndShell(p.Session); err == nil {
		t.Fatal("exchange reused")
	}
}

func TestLexicalExchangeValidatesWholeWriteTransaction(t *testing.T) {
	p := lexicalProgram(t)
	first := Cell[int8](p.Bindings, "a", "a", KindScalar)
	second := Cell[int8](p.Bindings, "z", "z", KindScalar)
	first.Value, first.Present = 1, true
	second.Value, second.Present = 2, true
	exchange, err := p.Bindings.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	p.Session.SetString("a", "9")
	p.Session.Set("z", Var{Kind: Indexed, List: []string{"128"}})
	p.Session.SetString("other", "keep")
	err = exchange.EndShell(p.Session)
	var boundary *LexicalWriteError
	if !errors.As(err, &boundary) || boundary.Name != "z" || boundary.ExitStatus() != 2 {
		t.Fatalf("unexpected error %v", err)
	}
	if first.Value != 1 || second.Value != 2 {
		t.Fatal("partial typed commit")
	}
	if _, ok := p.Session.Get("a"); ok {
		t.Fatal("overlay leaked after error")
	}
	if _, ok := p.Session.Get("z"); ok {
		t.Fatal("failed overlay leaked")
	}
	lexicalValue(t, p.Bindings.CaptureNames(nil), p.Session, "other", "keep", true)
}

func TestLexicalScalarWritesPreserveNativeTypes(t *testing.T) {
	type tiny int8
	type count uint16
	type flag bool
	type label string
	for _, tc := range []struct {
		initial any
		text    string
		want    any
	}{
		{tiny(0), "-128", tiny(-128)}, {tiny(0), "128", tiny(-128)}, {count(0), "-1", count(65535)}, {flag(false), "1", flag(false)}, {count(0), "65535", count(65535)},
		{flag(false), "true", flag(true)}, {label(""), "two words\n", label("two words\n")},
		{int64(0), "-9223372036854775808", int64(-9223372036854775808)},
		{uint64(0), "18446744073709551615", uint64(18446744073709551615)},
	} {
		got, err := lexicalScalarWrite("value", reflect.TypeOf(tc.initial), Var{Str: tc.text})
		if err != nil || !reflect.DeepEqual(got.Interface(), tc.want) {
			t.Fatalf("%T: %v %v", tc.initial, got, err)
		}
	}
	for _, tc := range []struct {
		initial any
		text    string
	}{{int(0), "text"}} {
		if _, err := lexicalScalarWrite("value", reflect.TypeOf(tc.initial), Var{Str: tc.text}); err == nil {
			t.Fatalf("accepted %T = %q", tc.initial, tc.text)
		}
	}
}

func TestLexicalRichIdentityAndUnsupportedMutation(t *testing.T) {
	p := lexicalProgram(t)
	cell := Cell[map[string]int](p.Bindings, "map", "data", KindObject)
	cell.Value, cell.Present = map[string]int{"n": 1}, true
	original := cell.Value
	exchange, err := p.Bindings.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err := exchange.EndShell(p.Session); err != nil {
		t.Fatal(err)
	}
	cell.Value["n"] = 2
	if original["n"] != 2 {
		t.Fatal("unchanged shell read replaced native map")
	}
	exchange, err = p.Bindings.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	p.Session.SetString("data", `{"n":9}`)
	if err := exchange.EndShell(p.Session); err == nil {
		t.Fatal("rich mutation silently decoded JSON")
	}
	if cell.Value["n"] != 2 {
		t.Fatal("failed rich mutation changed native value")
	}
}

func TestLexicalProjectionFailureHasNoSessionEffects(t *testing.T) {
	p := lexicalProgram(t)
	first := Cell[int](p.Bindings, "a", "a", KindScalar)
	first.Value, first.Present = 3, true
	bad := Cell[map[string]int](p.Bindings, "z", "z", KindScalar)
	bad.Value, bad.Present = map[string]int{}, true
	if _, err := p.Bindings.BeginShell(p.Session); err == nil {
		t.Fatal("invalid scalar projection accepted")
	}
	if _, ok := p.Session.Get("a"); ok {
		t.Fatal("partial overlay")
	}
}

func TestLexicalForkUsesOneSnapshotForAllRoots(t *testing.T) {
	p := lexicalProgram(t)
	root := Cell[int](p.Bindings, "root", "root", KindScalar)
	alias := Cell[*int](p.Bindings, "alias", "alias", KindPointer)
	root.Value, root.Present = 7, true
	alias.Value, alias.Present = &root.Value, true
	child := p.Child(context.Background(), nil)
	if child.Bindings == p.Bindings {
		t.Fatal("child shared registry")
	}
	childRoot := Cell[int](child.Bindings, "root", "root", KindScalar)
	childAlias := Cell[*int](child.Bindings, "alias", "alias", KindPointer)
	if childRoot.Present || childAlias.Present || childRoot.Value != 0 || childAlias.Value != nil {
		t.Fatal("Fork copied live values")
	}
	// Include a non-cell root in the same graph to prove cross-root rebasing.
	outside := &root.Value
	var copiedOutside *int
	snapshot := NewSnapshot(p.Readonly)
	for _, err := range []error{Capture(snapshot, &root.Value, &childRoot.Value), Capture(snapshot, &alias.Value, &childAlias.Value), Capture(snapshot, &outside, &copiedOutside)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := snapshot.Clone(); err != nil {
		t.Fatal(err)
	}
	childRoot.Present, childAlias.Present = root.Present, alias.Present
	child.Readonly = snapshot.Readonly()
	if childAlias.Value != &childRoot.Value || copiedOutside != &childRoot.Value {
		t.Fatal("split cell aliases")
	}
	*childAlias.Value = 9
	if root.Value != 7 || childRoot.Value != 9 {
		t.Fatal("parent/child value ownership lost")
	}
	captured := child.Bindings.CaptureNames(map[string]string{"root": "root"})
	lexicalValue(t, captured, child.Session, "root", "9", true)
}

func TestLexicalEntriesAndTaskForksAreIndependent(t *testing.T) {
	const workers = 16
	var wg sync.WaitGroup
	failures := make(chan error, workers)
	for i := range workers {
		wg.Go(func() {
			p, err := NewProgram(WithStdio(nil, new(bytes.Buffer), new(bytes.Buffer)))
			if err != nil {
				failures <- err
				return
			}
			defer p.Session.Close()
			root := Cell[int](p.Bindings, "same-id", "x", KindScalar)
			root.Value, root.Present = i, true
			view := p.Bindings.CaptureNames(map[string]string{"x": "same-id"})
			entered, err := p.Enter(Site{Name: "ordinary"}, false)
			if err != nil || entered.Bindings != p.Bindings || p.Block().Bindings != p.Bindings {
				failures <- fmt.Errorf("sequential view lost binding identity")
				return
			}
			task := p.Session.Go(func(ctx context.Context, session *Session) error {
				child := p.Child(ctx, session)
				copied := Cell[int](child.Bindings, "same-id", "x", KindScalar)
				snapshot := NewSnapshot(p.Readonly)
				if err := Capture(snapshot, &root.Value, &copied.Value); err != nil {
					return err
				}
				if err := snapshot.CloneContext(ctx); err != nil {
					return err
				}
				copied.Present = root.Present
				session.Arm()
				copied.Value += 1000
				value, present, err := child.Bindings.ShellValue(session, "x")
				if err != nil || !present || value != fmt.Sprint(i+1000) {
					return fmt.Errorf("wrong child value %q %v %v", value, present, err)
				}
				return nil
			})
			for n := 0; n < 10; n++ {
				exchange, err := view.BeginShell(p.Session)
				if err != nil {
					failures <- err
					return
				}
				p.Session.SetString("x", fmt.Sprint(i+n))
				if err := exchange.EndShell(p.Session); err != nil {
					failures <- err
					return
				}
			}
			if err := task.Wait(); err != nil {
				failures <- err
				return
			}
			if root.Value != i+9 {
				failures <- fmt.Errorf("wrong root value %d", root.Value)
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestLexicalRegisterPreservesActivationAndNamedResultStorage(t *testing.T) {
	p := lexicalProgram(t)
	var views []*LexicalBindings
	var addresses []*int
	for n := 0; n < 3; n++ {
		value, present := n, true
		if err := Register(p.Bindings, "iteration:x", "x", &value, &present, KindScalar); err != nil {
			t.Fatal(err)
		}
		views = append(views, p.Bindings.CaptureNames(map[string]string{"x": "iteration:x"}))
		addresses = append(addresses, &value)
	}
	for i, view := range views {
		lexicalValue(t, view, p.Session, "x", fmt.Sprint(i), true)
	}
	*addresses[0] = 9
	lexicalValue(t, views[0], p.Session, "x", "9", true)
	lexicalValue(t, views[2], p.Session, "x", "2", true)
	named := func() (result int) {
		present := true
		if err := Register(p.Bindings, "call:result", "result", &result, &present, KindScalar); err != nil {
			t.Fatal(err)
		}
		defer func() {
			exchange, err := p.Bindings.CaptureNames(map[string]string{"result": "call:result"}).BeginShell(p.Session)
			if err != nil {
				t.Fatal(err)
			}
			p.Session.SetString("result", "42")
			if err := exchange.EndShell(p.Session); err != nil {
				t.Fatal(err)
			}
		}()
		return 7
	}
	if got := named(); got != 42 {
		t.Fatalf("named result address changed: %d", got)
	}
}

func TestLexicalRegisteredRootsShareSnapshotAndReadonlyIdentity(t *testing.T) {
	p := lexicalProgram(t)
	parentValue, present := 7, true
	if err := Register(p.Bindings, "local", "x", &parentValue, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	if err := p.Readonly.Mark("x", &parentValue); err != nil {
		t.Fatal(err)
	}
	child := p.Child(nil, nil)
	var childValue int
	childPresent := present
	if err := Register(child.Bindings, "local", "x", &childValue, &childPresent, KindScalar); err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot(p.Readonly)
	if err := Capture(snapshot, &parentValue, &childValue); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Clone(); err != nil {
		t.Fatal(err)
	}
	child.Readonly = snapshot.Readonly()
	lexicalValue(t, child.Bindings, child.Session, "x", "7", true)
	if err := child.Readonly.CheckAssign(&childValue); err == nil {
		t.Fatal("readonly root identity lost")
	}
	wrong, presentWrong := "wrong", true
	if err := Register(child.Bindings, "local", "x", &wrong, &presentWrong, KindScalar); err == nil {
		t.Fatal("registration changed an ID's native type")
	}
	lexicalValue(t, child.Bindings, child.Session, "x", "7", true)
}

func TestLexicalExchangeCleanupDoesNotConsumeSourcePanic(t *testing.T) {
	p := lexicalProgram(t)
	cell := Cell[int](p.Bindings, "x", "x", KindScalar)
	cell.Value, cell.Present = 7, true
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		exchange, err := p.Bindings.BeginShell(p.Session)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := exchange.EndShell(p.Session); err != nil {
				t.Error(err)
			}
		}()
		p.Session.SetString("x", "9")
		panic("source panic")
	}()
	if recovered != "source panic" || cell.Value != 9 {
		t.Fatalf("panic=%v cell=%v", recovered, cell)
	}
	if _, present := p.Session.Get("x"); present {
		t.Fatal("overlay survived unwinding")
	}
}

func TestLexicalUnsupportedShellWritesRetainCells(t *testing.T) {
	for _, action := range []string{"unset", "array"} {
		t.Run(action, func(t *testing.T) {
			p := lexicalProgram(t)
			cell := Cell[int](p.Bindings, "x", "x", KindScalar)
			cell.Value, cell.Present = 7, true
			exchange, err := p.Bindings.BeginShell(p.Session)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "unset":
				p.Session.Unset("x")
			case "array":
				p.Session.Set("x", Var{Kind: Indexed, List: []string{"9"}})
			}
			if err := exchange.EndShell(p.Session); err == nil {
				t.Fatal("unsupported shell write accepted")
			}
			if cell.Value != 7 || !cell.Present {
				t.Fatal("unsupported write changed cell")
			}
			if _, ok := p.Session.Get("x"); ok {
				t.Fatal("temporary overlay leaked")
			}
		})
	}
}
