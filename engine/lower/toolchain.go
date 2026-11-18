package lower

import (
	"fmt"
	"go/version"
	"runtime"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// CodeToolchain identifies a compiler build that cannot check a required source
// feature. It is a build capability diagnostic, not a source semantic rejection.
const CodeToolchain = "LOWER-ETOOLCHAIN"

// CheckToolchain checks the Go checker compiled into this process. Changing PATH
// or GOTOOLCHAIN after building the compiler cannot upgrade that checker.
// Independent method type parameters require a Go 1.27.0 or newer build;
// ordinary functions and generic receiver parameters do not require this check.
func CheckToolchain(file *syntax.File) error {
	return checkToolchainVersion(file, runtime.Version())
}

func supportsMethodTypeParams(buildVersion string) bool {
	return version.IsValid(buildVersion) && version.Compare(buildVersion, "go1.27.0") >= 0
}

func checkToolchainVersion(file *syntax.File, buildVersion string) error {
	if file == nil || supportsMethodTypeParams(buildVersion) {
		return nil
	}
	var diagnostics ErrorList
	syntax.Walk(file, func(node syntax.Node) bool {
		method, ok := node.(*syntax.BashPPFuncDecl)
		if ok && method.Receiver != nil && len(method.TypeParams) != 0 {
			diagnostics = append(diagnostics, Diagnostic{
				Code: CodeToolchain,
				Msg:  fmt.Sprintf("independent method type parameters require a compiler built with Go 1.27.0 or newer (built with %s)", buildVersion),
				Node: "BashPPFuncDecl", Pos: method.Pos(),
			})
		}
		return true
	})
	if len(diagnostics) != 0 {
		return diagnostics
	}
	return nil
}
