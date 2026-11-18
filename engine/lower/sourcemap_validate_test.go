package lower_test

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A corrupted or truncated map must fail closed: every mutation of a valid
// result's map is rejected by ValidateMappings, so a consumer never trusts
// positions that no longer match the generated source or the recorded inputs.
func TestSourceMapValidateFailsClosed(t *testing.T) {
	good := compileGoSource(t, "main.go", userDirectiveSrc)
	if err := good.ValidateMappings(); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if len(good.Mappings) == 0 || len(good.Sources) == 0 {
		t.Fatalf("result has no mappings or sources to corrupt: %+v", good)
	}
	corrupt := []struct {
		name string
		mut  func(r *lower.Result)
	}{
		{"entry removed", func(r *lower.Result) {
			r.Mappings = r.Mappings[:len(r.Mappings)-1]
		}},
		{"line zero", func(r *lower.Result) {
			r.Mappings[0].GoLine = 0
		}},
		{"line beyond generated source", func(r *lower.Result) {
			r.Mappings[0].GoLine = 100000
		}},
		{"column beyond generated line", func(r *lower.Result) {
			r.Mappings[0].GoCol = 100000
		}},
		{"source renamed", func(r *lower.Result) {
			r.Mappings[0].Source = "elsewhere.go"
		}},
		{"file offset drifted", func(r *lower.Result) {
			r.Mappings[0].SourceOffset++
		}},
		{"position outside every source", func(r *lower.Result) {
			m := &r.Mappings[0]
			m.Pos = syntax.NewPos(r.Sources[0].Base+r.Sources[0].Size+100, m.Pos.Line(), m.Pos.Col())
		}},
	}
	for _, tc := range corrupt {
		t.Run(tc.name, func(t *testing.T) {
			r := *good
			r.Mappings = append([]lower.Mapping(nil), good.Mappings...)
			tc.mut(&r)
			if err := r.ValidateMappings(); err == nil {
				t.Errorf("corrupted map (%s) validated", tc.name)
			}
		})
	}
}
