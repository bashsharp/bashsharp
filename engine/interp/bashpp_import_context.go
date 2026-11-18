package interp

import (
	"context"
	"encoding/json"
	"go/build"
	"os"
	"os/exec"
)

// bashPPImportListTarget adds the source-directory vendor context omitted by
// go list of a bare import path in GOPATH mode. The original path remains the
// language-visible import and the input to capability/visibility validation.
func bashPPImportListTarget(ctx context.Context, req bashPPEvalRequest, path string) (string, error) {
	command := exec.CommandContext(ctx, req.internalBuildGo(), "env", "-json", "GOMOD", "GOPATH", "GOROOT")
	command.Dir, command.Env = req.Dir, req.internalBuildEnv()
	data, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return path, nil // Preserve the original go-list diagnostic.
	}
	var env struct{ GOMOD, GOPATH, GOROOT string }
	if json.Unmarshal(data, &env) != nil || env.GOMOD != "" {
		return path, nil
	}
	resolver := build.Default
	resolver.GOPATH, resolver.GOROOT = env.GOPATH, env.GOROOT
	// A filesystem callback selects go/build's structural resolver. It must not
	// consult process-global GO111MODULE: each runner has its own request env.
	resolver.IsDir = func(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
	pkg, err := resolver.Import(path, req.Dir, build.FindOnly)
	if err != nil {
		return path, nil
	}
	return pkg.ImportPath, nil
}
