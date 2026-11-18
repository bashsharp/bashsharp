//go:build full

package interp

// Sprint: #379; Story: #1516; Story-ID: f3de4d65a886

import "testing"

// The general method-callback bridge reviews the standard library by name
// and requires a source proof for everything else; the split rests on the
// callable's package as nativeSliceCallable spells it.
func TestS379CallablePackageClassification(t *testing.T) {
	for callable, want := range map[string]struct {
		pkg      string
		standard bool
	}{
		"fmt.Println":                            {"fmt", true},
		"*log.Logger.Printf":                     {"log", true},
		"sync.Once.Do":                           {"sync", true},
		"reflect.ValueOf":                        {"reflect", true},
		"encoding/json.Marshal":                  {"encoding/json", true},
		"*cmd/internal/obj.Link.AllPos":          {"cmd/internal/obj", true},
		"golang.org/x/tour/pic.ShowImage":        {"golang.org/x/tour/pic", false},
		"example.com/callback-test/dep.Retain":   {"example.com/callback-test/dep", false},
		"*example.com/s379sync/dep.Keeper.Write": {"example.com/s379sync/dep", false},
		"range-iterator":                         {"range-iterator", true},
	} {
		if got := bashPPCallablePackage(callable); got != want.pkg {
			t.Errorf("bashPPCallablePackage(%q) = %q, want %q", callable, got, want.pkg)
		}
		if got := bashPPStandardImportPath(bashPPCallablePackage(callable)); got != want.standard {
			t.Errorf("bashPPStandardImportPath(%q) = %v, want %v", callable, got, want.standard)
		}
	}
	if bashPPStandardImportPath("") {
		t.Error("an empty path is not a standard-library package")
	}
	if !nativeValueRetainer("reflect.ValueOf") || nativeValueRetainer("fmt.Println") {
		t.Error("reflect.ValueOf is the reviewed value retainer")
	}
}
