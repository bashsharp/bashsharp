package lower

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/build"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type listError struct {
	ImportStack []string `json:"ImportStack"`
	Pos         string   `json:"Pos"`
	Err         string   `json:"Err"`
}

type listPackage struct {
	Dir        string            `json:"Dir"`
	ImportPath string            `json:"ImportPath"`
	Export     string            `json:"Export"`
	Error      *listError        `json:"Error"`
	DepsErrors []*listError      `json:"DepsErrors"`
	ImportMap  map[string]string `json:"ImportMap"`
}

type moduleMetadata struct {
	Path string `json:"Path"`
	Dir  string `json:"Dir"`
}

type moduleImporter struct {
	dir           string
	packages      map[string]*listPackage
	importMap     map[string]string
	callerPath    string
	mu            sync.Mutex
	delegate      types.Importer
	gopathContext *build.Context
	sdk           *goSDK
	sdkErr        error
}

// newModuleImporter creates a types.Importer that invokes `go list -export -deps -json`
// in the specified directory using an installed Go SDK resolved by resolveGoSDK.
// The SDK supplies a coherent go command and GOROOT for module, GOPATH and
// internal-package decisions alike; a resolution failure is reported on first
// import rather than being turned into a relative "bin/go" exec.
// The returned importer also implements types.ImporterFrom and enforces internal package visibility.
func newModuleImporter(dir string) types.Importer {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir, _ = filepath.EvalSymlinks(dir)
	m := &moduleImporter{
		dir:       dir,
		packages:  make(map[string]*listPackage),
		importMap: make(map[string]string),
	}
	m.sdk, m.sdkErr = resolveGoSDK(dir)
	m.callerPath = determineCallerPath(dir, m.sdk)
	// Module-aware go list resolves module/workspace vendoring itself. In GOPATH
	// mode the standard structural resolver supplies the importing-directory
	// context even when that directory contains only Bash++ source. The SDK
	// already reported GOMOD and GOPATH for this directory.
	if m.sdk != nil && m.sdk.GOMOD == "" {
		ctx := build.Default
		ctx.GOPATH, ctx.GOROOT = m.sdk.GOPATH, m.sdk.Root
		m.gopathContext = &ctx
	}
	m.delegate = importer.ForCompiler(token.NewFileSet(), "gc", m.lookup)
	return m
}

func determineCallerPath(dir string, sdk *goSDK) string {
	cleanDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		cleanDir = filepath.Clean(dir)
	}

	if sdk != nil {
		cleanRoot, err := filepath.EvalSymlinks(sdk.Root)
		if err != nil {
			cleanRoot = filepath.Clean(sdk.Root)
		}
		srcRoot := filepath.Join(cleanRoot, "src")
		if rel, err := filepath.Rel(srcRoot, cleanDir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}

		cmd := exec.Command(sdk.Bin, "list", "-m", "-json")
		cmd.Dir = cleanDir
		cmd.Env = sdk.env(os.Environ())
		var stdout bytes.Buffer
		cmd.Stdout = &stdout

		if cmd.Run() == nil {
			decoder := json.NewDecoder(&stdout)
			var bestMod moduleMetadata
			bestModLen := -1

			for decoder.More() {
				var mod moduleMetadata
				if err := decoder.Decode(&mod); err == nil && mod.Path != "" && mod.Dir != "" {
					cleanModDir, err := filepath.EvalSymlinks(mod.Dir)
					if err != nil {
						cleanModDir = filepath.Clean(mod.Dir)
					}
					if cleanDir == cleanModDir || strings.HasPrefix(cleanDir, cleanModDir+string(filepath.Separator)) {
						if len(cleanModDir) > bestModLen {
							bestMod = mod
							bestModLen = len(cleanModDir)
						}
					}
				}
			}

			if bestModLen >= 0 {
				cleanModDir, err := filepath.EvalSymlinks(bestMod.Dir)
				if err != nil {
					cleanModDir = filepath.Clean(bestMod.Dir)
				}
				rel, err := filepath.Rel(cleanModDir, cleanDir)
				if err == nil && !strings.HasPrefix(rel, "..") {
					if rel == "." {
						return bestMod.Path
					}
					return filepath.ToSlash(filepath.Join(bestMod.Path, rel))
				}
			}
		}
	}

	gopathEnv := os.Getenv("GOPATH")
	if gopathEnv == "" {
		if home, err := os.UserHomeDir(); err == nil {
			gopathEnv = filepath.Join(home, "go")
		}
	}

	for _, gopath := range filepath.SplitList(gopathEnv) {
		if gopath == "" {
			continue
		}
		srcDir := filepath.Join(gopath, "src")
		cleanSrcDir, err := filepath.EvalSymlinks(srcDir)
		if err != nil {
			cleanSrcDir = filepath.Clean(srcDir)
		}

		rel, err := filepath.Rel(cleanSrcDir, cleanDir)
		if err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			return filepath.ToSlash(rel)
		}
	}

	return filepath.Base(cleanDir)
}

func isDirInGOROOT(dir, goroot string) bool {
	if goroot == "" {
		return false
	}
	cleanDir, err1 := filepath.EvalSymlinks(dir)
	if err1 != nil {
		cleanDir = filepath.Clean(dir)
	}
	cleanGoroot, err2 := filepath.EvalSymlinks(goroot)
	if err2 != nil {
		cleanGoroot = filepath.Clean(goroot)
	}

	rel, err := filepath.Rel(cleanGoroot, cleanDir)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

func checkInternalVisibility(targetPath, callerPath, callerDir, goroot string) error {
	parts := strings.Split(targetPath, "/")
	internalIdx := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "internal" {
			internalIdx = i
			break
		}
	}
	if internalIdx == -1 {
		return nil
	}

	parentPrefix := strings.Join(parts[:internalIdx], "/")

	if parentPrefix == "" {
		if !isDirInGOROOT(callerDir, goroot) {
			return fmt.Errorf("use of internal package %s not allowed", targetPath)
		}
		return nil
	}

	if callerPath == "" {
		return fmt.Errorf("use of internal package %s not allowed", targetPath)
	}

	if callerPath == parentPrefix || strings.HasPrefix(callerPath, parentPrefix+"/") {
		return nil
	}

	return fmt.Errorf("use of internal package %s not allowed", targetPath)
}

func (m *moduleImporter) Import(path string) (*types.Package, error) {
	return m.ImportFrom(path, "", 0)
}

func (m *moduleImporter) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	m.mu.Lock()
	caller := m.callerPath
	callerDir := m.dir
	sdk, sdkErr := m.sdk, m.sdkErr
	m.mu.Unlock()

	if sdkErr != nil {
		return nil, fmt.Errorf("cannot resolve %q: %w", path, sdkErr)
	}

	if err := checkInternalVisibility(path, caller, callerDir, sdk.Root); err != nil {
		return nil, err
	}

	if from, ok := m.delegate.(types.ImporterFrom); ok {
		return from.ImportFrom(path, srcDir, mode)
	}
	return m.delegate.Import(path)
}

// SourcePackageFiles lists a package's build-constrained Go source files in
// the resolved SDK (gosource.SourcePackageLister): the map importer
// re-checks a dependency from them when its export data reaches a mapped
// package (cmd/go's test-variant rebuild). Packages with cgo files, and
// packages the standard structural resolver cannot place, report an error
// and stay on their export data.
func (m *moduleImporter) SourcePackageFiles(path, srcDir string) (string, []string, error) {
	m.mu.Lock()
	sdk, sdkErr, dir := m.sdk, m.sdkErr, m.dir
	m.mu.Unlock()
	if sdkErr != nil {
		return "", nil, sdkErr
	}
	ctx := build.Default
	ctx.GOROOT, ctx.GOPATH = sdk.Root, sdk.GOPATH
	if srcDir == "" {
		srcDir = dir
	}
	// go/build runs `go list` in ctx.Dir, or in the process working directory
	// when that is empty; the module that answers must be the importing
	// directory's, wherever the embedding process is running.
	if filepath.IsAbs(srcDir) {
		ctx.Dir = srcDir
	}
	pkg, err := ctx.Import(path, srcDir, 0)
	if err != nil {
		return "", nil, err
	}
	if len(pkg.CgoFiles) > 0 {
		return "", nil, fmt.Errorf("package %q has cgo files", path)
	}
	return pkg.Dir, append([]string(nil), pkg.GoFiles...), nil
}

func (m *moduleImporter) lookup(path string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	actualPath := path
	if m.gopathContext != nil {
		if resolved, err := m.gopathContext.Import(path, m.dir, build.FindOnly); err == nil {
			actualPath = resolved.ImportPath
			// Preserve vendor precedence even if an unvendored package also exists.
			m.importMap[path] = actualPath
		}
	}
	if mapped, ok := m.importMap[path]; ok {
		actualPath = mapped
	}

	if _, ok := m.packages[actualPath]; !ok {
		if err := m.loadLocked(actualPath); err != nil {
			if errDot := m.loadLocked("."); errDot == nil {
				if mapped, ok := m.importMap[path]; ok {
					actualPath = mapped
				}
			} else {
				return nil, err
			}
		}
	}

	pkg, ok := m.packages[actualPath]
	if !ok {
		return nil, fmt.Errorf("package %q not found in go list output", path)
	}

	if pkg.Error != nil {
		return nil, fmt.Errorf("%s", pkg.Error.Err)
	}
	if len(pkg.DepsErrors) > 0 {
		return nil, fmt.Errorf("%s", pkg.DepsErrors[0].Err)
	}
	if pkg.Export == "" {
		return nil, fmt.Errorf("package %q has no export data", path)
	}

	return os.Open(pkg.Export)
}

func (m *moduleImporter) loadLocked(target string) error {
	if m.sdkErr != nil {
		return m.sdkErr
	}

	cmd := exec.Command(m.sdk.Bin, "list", "-export", "-deps", "-json", target)
	cmd.Dir = m.dir
	cmd.Env = m.sdk.env(os.Environ())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	decoder := json.NewDecoder(&stdout)
	for decoder.More() {
		var pkg listPackage
		if err := decoder.Decode(&pkg); err != nil {
			if runErr != nil {
				return fmt.Errorf("go list failed using %s: %w\n%s", m.sdk.describe(), runErr, stderr.String())
			}
			return fmt.Errorf("failed to decode go list output: %w", err)
		}
		if pkg.ImportPath != "" {
			m.packages[pkg.ImportPath] = &pkg
			for k, v := range pkg.ImportMap {
				m.importMap[k] = v
			}
		}
	}

	pkg, ok := m.packages[target]
	if !ok {
		if mapped, has := m.importMap[target]; has {
			pkg, ok = m.packages[mapped]
		}
	}

	if !ok && runErr != nil {
		return fmt.Errorf("go list failed using %s: %w\n%s", m.sdk.describe(), runErr, stderr.String())
	}
	if ok && pkg.Export == "" && pkg.Error == nil && len(pkg.DepsErrors) == 0 && runErr != nil {
		return fmt.Errorf("go build failed using %s: %w\n%s", m.sdk.describe(), runErr, stderr.String())
	}

	return nil
}

// NewModuleImporter resolves Go dependency export data in dir using the same
// SDK, module, workspace, vendor and internal visibility rules as Compile.
// It does not execute the importing program. Callers loading unchanged Go source
// can provide this importer to their Go type checker.
func NewModuleImporter(dir string) types.Importer { return newModuleImporter(dir) }

// ImportFromPackage is gosource's IdentityImporter: the explicit package map
// has already decided internal visibility on the importer's DECLARED
// identity (the compiler's -p), so this resolves the path policy-free — the
// directory rule of ImportFrom, keyed on where the importing files happen
// to sit, is exactly what would refuse an import the identity admits. Module,
// workspace, vendor and GOPATH resolution are the delegate's, unchanged.
// identity is the importer's declared path, for diagnostics.
func (m *moduleImporter) ImportFromPackage(path, identity, srcDir string) (*types.Package, error) {
	m.mu.Lock()
	sdkErr := m.sdkErr
	m.mu.Unlock()
	if sdkErr != nil {
		return nil, fmt.Errorf("cannot resolve %q for %q: %w", path, identity, sdkErr)
	}
	if from, ok := m.delegate.(types.ImporterFrom); ok {
		return from.ImportFrom(path, srcDir, 0)
	}
	return m.delegate.Import(path)
}
