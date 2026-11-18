package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourcePointerCallResult(t *testing.T) {
	compareSprint151PointerWithGo(t, "call_pointer.go")
}

func TestGoSourceNilPointerReceiver(t *testing.T) {
	compareSprint151PointerWithGo(t, "nil_receiver.go")
}

func TestGoSourcePointerReceiverMethodValue(t *testing.T) {
	compareSprint151PointerWithGo(t, "method_value.go")
}

func TestGoSourceNewArraySlice(t *testing.T) {
	compareSprint151PointerWithGo(t, "new_array_slice.go")
}

func TestGoSourceStringIndexAndSliceStructuredConsumers(t *testing.T) {
	compareSprint151PointerWithGo(t, "string_index_slice.go")
}

func TestGoSourceNilValues(t *testing.T) {
	compareSprint151PointerWithGo(t, "nil_values.go")
}

func TestGoSourceConversionIndex(t *testing.T) {
	compareSprint151PointerWithGo(t, "conversion_index.go")
}

func TestGoSourceTypeAssertionSelector(t *testing.T) {
	compareSprint151PointerWithGo(t, "assert_selector.go")
}

func TestGoSourcePointerCallAssignment(t *testing.T) {
	compareSprint151PointerWithGo(t, "call_pointer_assign.go")
}

func compareSprint151PointerWithGo(t *testing.T, name string) {
	t.Helper()
	path := filepath.Join("..", "gosource", "testdata", "sprint151", "pointers", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := exec.Command("go", "run", path).Output()
	if err != nil {
		t.Fatalf("go run: %v", err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: filepath.Base(path), Data: data}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil || stderr.Len() != 0 {
		t.Fatalf("interpreter: err=%v stderr=%q", err, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("stdout differs from go run\ninterpreter: %q\ngo run:      %q", stdout.Bytes(), want)
	}
}
