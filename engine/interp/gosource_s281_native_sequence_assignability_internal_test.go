//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestS281NativeSequenceMaterializationChecksSourceType keeps the sequence
// materializer behind the dependency helper's authenticated assignability
// check. Element-by-element conversion cannot reject an incompatible empty
// slice, and cannot distinguish arrays whose element types match but lengths
// differ.
func TestS281NativeSequenceMaterializationChecksSourceType(t *testing.T) {
	var r *Runner
	var checks []error
	probed := false
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		probed = true
		stringType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}
		uint64Type := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "uint64"}}
		byteType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "byte"}}
		pairOfStrings := &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "2"}, Element: stringType}
		pairOfUint64 := &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "2"}, Element: uint64Type}
		sliceOfStrings := &syntax.BashPPCollectionType{Kind: "slice", Element: stringType}
		dwarfFieldType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "dwarf.Field"}}
		tests := []struct {
			name          string
			cell          string
			expected      syntax.BashPPTypeExpr
			wantErr       bool
			wantChildKind string
		}{
			{"non-empty compatible", "ranges", &syntax.BashPPCollectionType{Kind: "slice", Element: pairOfUint64}, false, "array"},
			{"nested slice compatible", "records", &syntax.BashPPCollectionType{Kind: "slice", Element: sliceOfStrings}, false, "slice"},
			{"imported element stays native", "fields", &syntax.BashPPCollectionType{Kind: "slice", Element: dwarfFieldType}, false, "native"},
			{"empty compatible", "empty", &syntax.BashPPCollectionType{Kind: "slice", Element: pairOfUint64}, false, ""},
			{"array compatible", "array", &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "32"}, Element: byteType}, false, ""},
			{"non-empty element mismatch", "ranges", &syntax.BashPPCollectionType{Kind: "slice", Element: pairOfStrings}, true, ""},
			{"empty element mismatch", "empty", &syntax.BashPPCollectionType{Kind: "slice", Element: pairOfStrings}, true, ""},
			{"array length mismatch", "array", &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "31"}, Element: byteType}, true, ""},
		}
		for _, test := range tests {
			native := r.bashPPNativeCellValue(test.cell)
			if native == nil || native.Kind != "handle" {
				checks = append(checks, fmt.Errorf("%s: missing native handle: %#v", test.name, native))
				continue
			}
			value, meta, handled, err := r.goSourceNativeSequenceContents(native, test.expected)
			if !handled || test.wantErr != (err != nil) || (test.wantErr && !strings.Contains(err.Error(), "cannot use native")) {
				checks = append(checks, fmt.Errorf("%s: handled=%v err=%v", test.name, handled, err))
				continue
			}
			if err != nil || test.wantChildKind == "" {
				continue
			}
			sequence, ok := value.([]any)
			if !ok || len(sequence) == 0 || meta == nil || len(meta.sequence) == 0 || meta.sequence[0] == nil || meta.sequence[0].kind != test.wantChildKind {
				checks = append(checks, fmt.Errorf("%s: value=%T meta=%#v", test.name, value, meta))
				continue
			}
			if test.wantChildKind == "native" {
				if _, ok := sequence[0].(*bashPPBridgeValue); !ok {
					checks = append(checks, fmt.Errorf("%s: imported child materialized as %T", test.name, sequence[0]))
				}
			} else if _, ok := sequence[0].([]any); !ok {
				checks = append(checks, fmt.Errorf("%s: nested sequence remained %T", test.name, sequence[0]))
			}
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import (
	"crypto/sha256"
	"debug/dwarf"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)
func main() {
	empty, emptyErr := new(dwarf.Data).Ranges(&dwarf.Entry{Field: []dwarf.Field{{Attr: dwarf.AttrName, Val: "no-ranges"}}})
	ranges, rangesErr := new(dwarf.Data).Ranges(&dwarf.Entry{Field: []dwarf.Field{
		{Attr: dwarf.AttrLowpc, Val: uint64(10)},
		{Attr: dwarf.AttrHighpc, Class: dwarf.ClassAddress, Val: uint64(20)},
	}})
	records, recordsErr := csv.NewReader(strings.NewReader("a,b\nc,d\n")).ReadAll()
	fields := (&dwarf.Entry{Field: []dwarf.Field{{Attr: dwarf.AttrLowpc, Val: uint64(10)}}}).Field
	array := sha256.Sum256([]byte("probe"))
	println("probe")
	fmt.Fprintf(io.Discard, "%v %v %v %v %v %v %v %v", empty, emptyErr, ranges, rangesErr, records, recordsErr, fields, array)
}`
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), p.File); err != nil {
		t.Fatal(err)
	}
	if !probed {
		t.Fatal("native sequence probe did not run")
	}
	for _, err := range checks {
		t.Error(err)
	}
}
