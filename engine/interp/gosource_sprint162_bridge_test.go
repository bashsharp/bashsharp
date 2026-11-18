//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func mustReadSprint162Bridge(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint162", "bridge", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGoSourceSprint162GenericBridgeTypes(t *testing.T) {
	differGoSource(t, mustReadSprint162Bridge(t, "generic_types.go.txt"), nil, "")
}

func TestGoSourceSprint162GenericBridgeJSON(t *testing.T) {
	differGoSource(t, mustReadSprint162Bridge(t, "generic_json.go.txt"), nil, "")
}

func TestGoSourceSprint162GenericBridgePositiveControl(t *testing.T) {
	differGoSource(t, mustReadSprint162Bridge(t, "generic_types_ref.go.txt"), nil, "")
}

func TestGoSourceSprint162GenericBridgeNegativeReferenceReceiver(t *testing.T) {
	got := runGoSourceRunnerError(t, mustReadSprint162Bridge(t, "generic_types_negative.go.txt"))
	if !strings.Contains(got, "wrote through reference storage") {
		t.Fatalf("wrong refusal for reference-bearing generic receiver: %q", got)
	}
}

func TestGoSourceSprint162NativeFieldSet(t *testing.T) {
	differGoSource(t, mustReadSprint162Bridge(t, "field_set.go.txt"), nil, "")
}

func TestGoSourceSprint162NativeFieldSetNegative(t *testing.T) {
	_, err := gosource.Parse(strings.NewReader(mustReadSprint162Bridge(t, "field_set_negative.go.txt")), "field_set_negative.go", gosource.Options{RunMain: true})
	if err == nil || !strings.Contains(err.Error(), "cannot use 1") {
		t.Fatalf("wrong field-set type error: %v", err)
	}
}

func TestGoSourceSprint162NativeVariableSet(t *testing.T) {
	differGoSource(t, mustReadSprint162Bridge(t, "var_set.go.txt"), nil, "")
}

func TestGoSourceSprint162NativeVariableSetNegative(t *testing.T) {
	_, err := gosource.Parse(strings.NewReader(mustReadSprint162Bridge(t, "var_set_negative.go.txt")), "var_set_negative.go", gosource.Options{RunMain: true})
	if err == nil || !strings.Contains(err.Error(), "cannot use \"wrong\"") {
		t.Fatalf("wrong var-set type error: %v", err)
	}
}

func TestGoSourceSprint162BridgeStringBytes(t *testing.T) {
	differGoSource(t, mustReadSprint162Bridge(t, "string_bytes.go.txt"), nil, "")
}
