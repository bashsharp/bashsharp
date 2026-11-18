package lower

import (
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"strings"
)

// The runtime's actual public declarations are part of the compiler build.
// Checking these declarations neither executes them nor reads installation or
// source-file paths when a generated program is compiled.
//
//go:embed shellrt/*.go
var runtimeDeclarations embed.FS

func (i bridgeImporter) importRuntime(path string) (*types.Package, error) {
	paths, err := fs.Glob(runtimeDeclarations, "shellrt/*.go")
	if err != nil {
		return nil, err
	}
	positions := token.NewFileSet()
	var files []*ast.File
	for _, name := range paths {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := runtimeDeclarations.ReadFile(name)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(positions, name, source, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	config := types.Config{Importer: i.fallback, IgnoreFuncBodies: true, DisableUnusedImportCheck: true}
	return config.Check(path, positions, files, nil)
}

func (i bridgeImporter) importShellExec(path string) (*types.Package, error) {
	positions := token.NewFileSet()
	imports, decls := "", ""
	if i.foreign {
		imports = `import interp "github.com/bashsharp/bashsharp/engine/interp"
import polyglot "github.com/bashsharp/bashsharp/engine/polyglot"
`
		decls = `func RunnerOptions(opts ...interp.RunnerOption) Option
func NewForeignIterator(module *polyglot.Module,name string,args []any) *rt.ForeignIterator
func NewPythonHandleIterator(handle *polyglot.Handle) *rt.ForeignIterator
`
	}
	source := `package shellexec
import rt "` + i.path + `"
` + imports + `type Option func()
func BashPP() Option
func New(options ...Option) rt.ShellFactory
` + decls
	file, err := parser.ParseFile(positions, "shellexec.go", source, 0)
	if err != nil {
		return nil, err
	}
	config := types.Config{Importer: i, IgnoreFuncBodies: true}
	return config.Check(path, positions, []*ast.File{file}, nil)
}
