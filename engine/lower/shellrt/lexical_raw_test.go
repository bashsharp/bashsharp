package shellrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func writeRaw(t *testing.T, p *Program, name, text string) {
	t.Helper()
	exchange, err := p.Bindings.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	p.Session.SetString(name, text)
	if err := exchange.EndShell(p.Session); err != nil {
		t.Fatal(err)
	}
}

// The same source is executed by the real interpreter. The runtime path below
// exercises helper observations explicitly; whole-compiler dispatch is a
// separate integration gate, not claimed by this test.
func rawScalarOracle[T any](t *testing.T, typ string, initial T, raw, expression string, operation func(T) any, want string) {
	t.Helper()
	source := fmt.Sprintf("func main() {\nvar x %s = 1\nx=%s\necho \"raw=$x\"\nprintln(x)\ny := %s\necho \"y=${y-unset}\"\n}\nmain()\n", typ, raw, expression)
	if typ == "bool" {
		source = strings.Replace(source, "bool = 1", "bool = true", 1)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "raw.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var sourceOut, sourceErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &sourceOut, &sourceErr))
	if err != nil {
		t.Fatal(err)
	}
	runErr := runner.Run(context.Background(), file)
	status := 0
	if runErr != nil {
		var exit interp.ExitStatus
		if !errors.As(runErr, &exit) {
			t.Fatal(runErr)
		}
		status = int(exit)
	}
	p := lexicalProgram(t)
	cell := Cell[T](p.Bindings, "x", "x", KindScalar)
	cell.Value, cell.Present = initial, true
	if err := p.Bindings.SetInfo("x", LexicalInfo{SourceType: typ}); err != nil {
		t.Fatal(err)
	}
	if typ == "float64" {
		if err := p.Bindings.NativeScalarWritten("x", constant.MakeInt64(1)); err != nil {
			t.Fatal(err)
		}
	}
	writeRaw(t, p, "x", raw)
	var nativeOut, nativeErr bytes.Buffer
	text, _, err := p.Bindings.ShellValue(p.Session, "x")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(&nativeOut, "raw="+text)
	printed, present, err := p.Bindings.PrintValue("x", ValueSite{Name: "x"})
	if err != nil || !present {
		t.Fatalf("print=%v/%v/%v", printed, present, err)
	}
	fmt.Fprintln(&nativeOut, printed)
	value, err := Load[T](p.Bindings, "x", ValueSite{File: "raw.bpp", Name: "x", Line: 6, Column: 6})
	nativeStatus := 0
	if err != nil {
		fmt.Fprintln(&nativeErr, err)
		fmt.Fprintln(&nativeOut, "y=unset") // failed binding stays absent; caller continues
		nativeStatus = 2
	} else {
		fmt.Fprintln(&nativeOut, "y="+fmt.Sprint(operation(value)))
	}
	if nativeOut.String() != sourceOut.String() || nativeErr.String() != sourceErr.String() || nativeStatus != status {
		t.Fatalf("source stdout=%q stderr=%q status=%d; helper stdout=%q stderr=%q status=%d", sourceOut.String(), sourceErr.String(), status, nativeOut.String(), nativeErr.String(), nativeStatus)
	}
	if sourceOut.String() != want {
		t.Fatalf("oracle changed: %q", sourceOut.String())
	}
}

func TestLexicalRawScalarSourceObservations(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		rawScalarOracle(t, "int", 1, "42", "x + 1", func(x int) any { return x + 1 }, "raw=42\n42\ny=43\n")
	})
	t.Run("octal", func(t *testing.T) {
		rawScalarOracle(t, "int", 1, "010", "x + 1", func(x int) any { return x + 1 }, "raw=010\n8\ny=9\n")
	})
	t.Run("float_spelling_integer", func(t *testing.T) {
		rawScalarOracle(t, "int", 1, "09", "x + 1", func(x int) any { return x + 1 }, "raw=09\n9\ny=10\n")
	})
	t.Run("invalid_integer", func(t *testing.T) {
		rawScalarOracle(t, "int", 1, "abc", "x + 1", func(x int) any { return x + 1 }, "raw=abc\nabc\ny=unset\n")
	})
	t.Run("narrow_result", func(t *testing.T) {
		rawScalarOracle(t, "int8", int8(1), "128", "x + 1", func(x int8) any { return x + 1 }, "raw=128\n128\ny=-127\n")
	})
	t.Run("float", func(t *testing.T) {
		rawScalarOracle(t, "float64", float64(1), "2.500", "x + 0.5", func(x float64) any { return x + 0.5 }, "raw=2.500\n2.5\ny=3\n")
	})
	t.Run("invalid_float", func(t *testing.T) {
		rawScalarOracle(t, "float64", float64(1), "abc", "x + 0.5", func(x float64) any { return x + 0.5 }, "raw=abc\nabc\ny=unset\n")
	})
	t.Run("boolean", func(t *testing.T) {
		rawScalarOracle(t, "bool", true, "anything", "!x", func(x bool) any { return !x }, "raw=anything\nfalse\ny=true\n")
	})
}

func TestLexicalInvalidLoadIsPositionedLazyAndNeverStale(t *testing.T) {
	p := lexicalProgram(t)
	cell := Cell[int](p.Bindings, "root", "x", KindScalar)
	cell.Value, cell.Present = 99, true
	pointer := &cell.Value
	writeRaw(t, p, "x", "abc")
	if cell.Value != 0 {
		t.Fatal("old native value survived invalid text")
	}
	site := ValueSite{File: "lazy.bpp", Name: "x", Line: 7, Column: 5, Offset: 41}
	calls := 0
	read := func() int { calls++; return MustValue(LoadAddress(p.Bindings, pointer, site)) }
	if false && read() > 0 {
		t.Fatal("unreachable")
	}
	if calls != 0 {
		t.Fatal("skipped operand evaluated")
	}
	value, err := TryValue(read)
	var diagnostic *ValueError
	if value != 0 || !errors.As(err, &diagnostic) || diagnostic.Site != site || diagnostic.Error() != "BASHPP-EEXPR-CONVERT: cannot convert String to int" {
		t.Fatalf("value=%v error=%v", value, err)
	}
	if calls != 1 {
		t.Fatal("read evaluated more than once")
	}
	*pointer = 7
	if err := p.Bindings.NativeWrittenAt(pointer); err != nil {
		t.Fatal(err)
	}
	if value, err := Load[int](p.Bindings, "root", site); err != nil || value != 7 {
		t.Fatalf("%v %v", value, err)
	}
}

func TestLexicalNativeWriteClearsEvenEqualRawSpelling(t *testing.T) {
	p := lexicalProgram(t)
	value, present := 1, true
	if err := Register(p.Bindings, "x", "x", &value, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	captured := p.Bindings.CaptureNames(map[string]string{"x": "x"})
	writeRaw(t, p, "x", "01")
	lexicalValue(t, captured, p.Session, "x", "01", true)
	value = 1
	if err := p.Bindings.NativeWritten("x"); err != nil {
		t.Fatal(err)
	}
	lexicalValue(t, captured, p.Session, "x", "1", true)
}

func TestLexicalWiderScalarIsAvailableBeforeNarrowing(t *testing.T) {
	source := "func main() {\nvar x int8 = 1\nx=128\ny := x / 2\np := x > 0\nprintln(y, p)\n}\nmain()\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "wide.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil || out.String() != "64 true\n" || diagnostic.Len() != 0 {
		t.Fatalf("oracle stdout=%q stderr=%q err=%v", out.String(), diagnostic.String(), err)
	}
	p := lexicalProgram(t)
	cell := Cell[int8](p.Bindings, "x", "x", KindScalar)
	cell.Value, cell.Present = 1, true
	writeRaw(t, p, "x", "128")
	scalar, _, err := p.Bindings.ScalarValue("x", ValueSite{})
	if err != nil {
		t.Fatal(err)
	}
	quotient := constant.BinaryOp(scalar, token.QUO, constant.MakeInt64(2))
	if quotient.ExactString() != "64" || !constant.Compare(scalar, token.GTR, constant.MakeInt64(0)) {
		t.Fatal("wide source scalar was narrowed")
	}
	// Load is explicitly native conversion, NOT a replacement for the wider
	// source operand in division/comparison.
	narrow, err := Load[int8](p.Bindings, "x", ValueSite{})
	if err != nil || narrow != -128 {
		t.Fatalf("%v %v", narrow, err)
	}
}

func TestLexicalRawMetadataFollowsCapturedAddressesAndFork(t *testing.T) {
	p := lexicalProgram(t)
	value, present := 1, true
	if err := Register(p.Bindings, "x", "x", &value, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	if err := Register(p.Bindings, "alias", "alias", &value, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	if err := p.Bindings.SetInfo("x", LexicalInfo{SourceType: "Count", Readonly: true}); err != nil {
		t.Fatal(err)
	}
	// Write only through x's view; the address alias shares raw provenance.
	onlyX := p.Bindings.CaptureNames(map[string]string{"x": "x"})
	exchange, err := onlyX.BeginShell(p.Session)
	if err != nil {
		t.Fatal(err)
	}
	p.Session.SetString("x", "abc")
	if err := exchange.EndShell(p.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := Load[int](p.Bindings, "alias", ValueSite{}); err == nil || err.Error() != "BASHPP-EEXPR-CONVERT: cannot convert String to Count" {
		t.Fatalf("%v", err)
	}
	child := p.Child(nil, nil)
	var childValue int
	childPresent := true
	if err := Register(child.Bindings, "x", "x", &childValue, &childPresent, KindScalar); err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot(p.Readonly)
	if err := Capture(snapshot, &value, &childValue); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Clone(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load[int](child.Bindings, "x", ValueSite{}); err == nil {
		t.Fatal("fork lost deferred conversion")
	}
	childValue = 8
	if err := child.Bindings.NativeWrittenAt(&childValue); err != nil {
		t.Fatal(err)
	}
	if _, err := Load[int](p.Bindings, "x", ValueSite{}); err == nil {
		t.Fatal("child changed parent raw metadata")
	}
	lexicalValue(t, child.Bindings, child.Session, "x", "8", true)
}

func TestLexicalRawConcurrentEntriesAndTaskCopies(t *testing.T) {
	const count = 12
	var wg sync.WaitGroup
	failures := make(chan error, count)
	for i := range count {
		wg.Go(func() {
			p, err := NewProgram(WithStdio(nil, new(bytes.Buffer), new(bytes.Buffer)))
			if err != nil {
				failures <- err
				return
			}
			defer p.Session.Close()
			cell := Cell[int](p.Bindings, "x", "x", KindScalar)
			cell.Value, cell.Present = i, true
			exchange, err := p.Bindings.BeginShell(p.Session)
			if err != nil {
				failures <- err
				return
			}
			p.Session.SetString("x", "010")
			if err := exchange.EndShell(p.Session); err != nil {
				failures <- err
				return
			}
			task := p.Session.Go(func(ctx context.Context, session *Session) error {
				child := p.Child(ctx, session)
				copy := Cell[int](child.Bindings, "x", "x", KindScalar)
				snap := NewSnapshot(p.Readonly)
				if err := Capture(snap, &cell.Value, &copy.Value); err != nil {
					return err
				}
				if err := snap.CloneContext(ctx); err != nil {
					return err
				}
				if err := child.Bindings.RebindSnapshot(snap); err != nil {
					return err
				}
				copy.Present = true
				session.Arm()
				got, err := Load[int](child.Bindings, "x", ValueSite{})
				if err != nil || got != 8 {
					return fmt.Errorf("child raw load %v %v", got, err)
				}
				copy.Value = i
				if err := child.Bindings.NativeWritten("x"); err != nil {
					return err
				}
				return nil
			})
			cell.Value = i + 100
			if err := p.Bindings.NativeWritten("x"); err != nil {
				failures <- err
				return
			}
			if err := task.Wait(); err != nil {
				failures <- err
				return
			}
			got, err := Load[int](p.Bindings, "x", ValueSite{})
			if err != nil || got != i+100 {
				failures <- fmt.Errorf("parent load %v %v", got, err)
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestLexicalPointerArgumentRetainsProvenanceAcrossEarlierNameCapture(t *testing.T) {
	p := lexicalProgram(t)
	earlier := p.Bindings.CaptureNames(nil)
	cell := Cell[int](p.Bindings, "later", "x", KindScalar)
	cell.Value, cell.Present = 7, true
	writeRaw(t, p, "x", "abc")
	lexicalValue(t, earlier, p.Session, "x", "", false)
	// The callee cannot look up x by name, but its explicitly supplied native
	// pointer still targets x's current typed/raw state.
	if value, err := LoadAddress(earlier, &cell.Value, ValueSite{Name: "argument"}); err == nil || value != 0 {
		t.Fatalf("stale alias=%v %v", value, err)
	}
	cell.Value = 4
	if err := earlier.NativeWrittenAt(&cell.Value); err != nil {
		t.Fatal(err)
	}
	if value, err := Load[int](p.Bindings, "later", ValueSite{}); err != nil || value != 4 {
		t.Fatalf("%v %v", value, err)
	}
	lexicalValue(t, p.Bindings, p.Session, "x", "4", true)
}

func TestLexicalSnapshotRebindsHiddenRawPointees(t *testing.T) {
	p := lexicalProgram(t)
	earlier := p.Bindings.CaptureNames(nil)
	cell := Cell[int](p.Bindings, "later", "x", KindScalar)
	cell.Value, cell.Present = 1, true
	writeRaw(t, p, "x", "abc")
	child := earlier.Fork()
	pointer := &cell.Value
	var copied *int
	snapshot := NewSnapshot(p.Readonly)
	if err := Capture(snapshot, &pointer, &copied); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Clone(); err != nil {
		t.Fatal(err)
	}
	if err := child.RebindSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if copied == pointer {
		t.Fatal("pointee not cloned")
	}
	if value, err := LoadAddress(child, copied, ValueSite{}); err == nil || value != 0 {
		t.Fatalf("hidden clone read=%v %v", value, err)
	}
	*copied = 6
	if err := child.NativeWrittenAt(copied); err != nil {
		t.Fatal(err)
	}
	if value, err := LoadAddress(child, copied, ValueSite{}); err != nil || value != 6 {
		t.Fatalf("%v %v", value, err)
	}
	if _, err := LoadAddress(p.Bindings, pointer, ValueSite{}); err == nil {
		t.Fatal("child metadata affected parent")
	}
	lexicalValue(t, child, p.Session, "x", "", false)
}
