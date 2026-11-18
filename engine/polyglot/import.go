package polyglot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qiangli/bash/pathconv"
)

// ImportRequest is the declarative input for a direct foreign package import.
// Alias is already resolved by the syntax layer, including any safe default.
//
// Module is either a dotted Python module path (`pkg.mod`) or a Python source
// file path (`./tools/x.py`, `/abs/x.py`, `C:/tools/x.py`); see
// [PythonFileImport]. A relative file path is resolved against the directory
// of Source — the importing Bash# file — never against the process working
// directory, so a script means the same file wherever it is run from.
type ImportRequest struct {
	Source, Language, Environment, Module, Alias string
	Environ                                      []string
}

// ImportPlan is an immutable description of a package import. Planning selects
// an environment but deliberately does not start a runtime or import Module.
//
// Path is the absolute, host-native path of a file import and empty for a
// module import. Module keeps the spelling the source wrote.
type ImportPlan struct {
	ID                      string
	Language, Module, Alias string
	Path                    string
	Environment             EnvironmentPlan
}

// Clone returns an independently owned copy of p.
func (p ImportPlan) Clone() ImportPlan {
	p.Environment = p.Environment.Clone()
	return p
}

// Identity is the plan's module identity: two plans with the same identity
// load the same Python module in the same environment and may share one
// worker, which is what gives `import python "./x.py" as a` and
// `import python "./x.py" as b` one module object, as CPython's sys.modules
// does for `import x as a; import x as b`. The alias is deliberately not part
// of it; the ID is.
func (p ImportPlan) Identity() string {
	module := p.Module
	if p.Path != "" {
		module = p.Path
	}
	return p.Language + "\x00" + module + "\x00" + p.Environment.Fingerprint
}

// PythonFileImport reports whether a Python import operand names a source
// file rather than a dotted module: it ends in `.py` (any case, as CPython's
// Windows finder accepts `.PY`) or carries a path separator. The syntax layer
// applies the same rule before planning; keep the two in step.
func PythonFileImport(module string) bool {
	return strings.HasSuffix(strings.ToLower(module), ".py") || strings.ContainsAny(module, `/\`)
}

// PythonFileImportPath resolves a file import operand against the importing
// source's directory into the absolute host-native path the worker will load.
// The windows flag selects Windows spelling rules so the conversion is
// testable on any host: a native drive path (`C:\x.py`, `C:/x.py`), the
// MSYS form (`/c/x.py`) and the WSL form (`/mnt/c/x.py`) all resolve to the
// same drive path; a relative operand joins onto sourceDir with the host
// separator. On other hosts a backslash is an ordinary filename byte, as it
// is for CPython.
func PythonFileImportPath(sourceDir, module string, windows bool) (string, error) {
	if !PythonFileImport(module) {
		return "", fmt.Errorf("polyglot: %q is not a Python source file path", module)
	}
	if !strings.HasSuffix(strings.ToLower(module), ".py") {
		// CPython's spec_from_file_location returns no spec for a location
		// without a source suffix; refuse the import rather than execute a
		// file the loader would not recognize.
		return "", fmt.Errorf("polyglot: Python file import %q must name a .py source", module)
	}
	resolved := pathconv.JoinAbsMode(sourceDir, module, windows)
	if windows && strings.Contains(resolved, "|") {
		// A backslash in a relative operand is a filename character that
		// NTFS cannot store (pathconv spells it '|'): no such file exists.
		return "", fmt.Errorf("polyglot: No such file or directory: '%s'", strings.ReplaceAll(resolved, "|", `\`))
	}
	if windows && runtime.GOOS != "windows" {
		// Testing Windows spelling off-host: filepath.Clean would treat `\`
		// as a filename byte, so clean the drive-relative part as a slash
		// path and restore the native separator by hand.
		volume := ""
		if len(resolved) >= 2 && resolved[1] == ':' {
			volume, resolved = resolved[:2], resolved[2:]
		}
		resolved = path.Clean(strings.ReplaceAll(resolved, `\`, "/"))
		return volume + strings.ReplaceAll(resolved, "/", `\`), nil
	}
	return filepath.Clean(resolved), nil
}

// PlanImport selects the immutable environment for one direct import without
// executing that environment or probing the requested module.
func PlanImport(request ImportRequest) (ImportPlan, error) {
	language := canonicalLanguage(request.Language)
	if language != "python" {
		return ImportPlan{}, fmt.Errorf("polyglot: unsupported import language %q", language)
	}
	if strings.TrimSpace(request.Module) == "" || strings.TrimSpace(request.Alias) == "" {
		return ImportPlan{}, errors.New("polyglot: import module and alias are required")
	}
	environment, err := DiscoverEnvironment(EnvironmentRequest{
		Source: request.Source, Language: language, Name: request.Environment, Environ: request.Environ,
	})
	if err != nil {
		return ImportPlan{}, err
	}
	file := ""
	if PythonFileImport(request.Module) {
		// The file is resolved against the importing source, but the
		// environment stays the project's one declared environment: a file
		// outside the project runs with the project's interpreter and
		// PYTHONPATH, never with an environment guessed from the file's own
		// directory.
		file, err = PythonFileImportPath(environment.SourceDir, request.Module, runtime.GOOS == "windows")
		if err != nil {
			return ImportPlan{}, err
		}
	}
	hash := sha256.Sum256([]byte(language + "\x00" + request.Module + "\x00" + file + "\x00" + request.Alias + "\x00" + environment.Fingerprint))
	return ImportPlan{
		ID: hex.EncodeToString(hash[:]), Language: language, Module: request.Module,
		Alias: request.Alias, Path: file, Environment: environment.Clone(),
	}, nil
}
