package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestCgoBridgeRefusesNonFunctionSelector(t *testing.T) {
	pkg := syntax.CgoPackage{Path: "example.com/p", Alias: "c0", Preamble: "/* int x; */\n", Symbols: []syntax.CgoSymbol{{Name: "x", Kind: "unsupported"}}}
	_, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{Imports: map[string]string{"c0": "C"}, CgoPackages: []syntax.CgoPackage{pkg}})
	if err == nil || !strings.Contains(err.Error(), `cgo package "example.com/p" selector C.x has unsupported kind "unsupported"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestCgoBridgeRefusesUnavailableToolchain(t *testing.T) {
	pkg := syntax.CgoPackage{Path: "example.com/p", Alias: "c0", Preamble: "/* int f(void); */\n", Symbols: []syntax.CgoSymbol{{Name: "f", Kind: "func", ResultUsed: true}}}
	_, err := bashPPCgoWrapperSource(context.Background(), bashPPEvalRequest{Go: t.TempDir() + "/missing-go"}, 0, pkg)
	if err == nil || !strings.Contains(err.Error(), `cgo unavailable for package "example.com/p"`) {
		t.Fatalf("error = %v", err)
	}
}
