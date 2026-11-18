package gosource_test

import (
	"go/build"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Use the actual mapped root, not a deliberately inconsistent external importer.
// go/types depends on internal/types/errors; go/importer must bind to the same
// rebuilt go/types variant even when it is imported first.
func TestS243MappedErrorsImporterIdentity(t *testing.T) {
	pkg, err := build.Default.Import("internal/types/errors", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	read := func(names []string) []gosource.Source {
		var result []gosource.Source
		for _, name := range names {
			path := filepath.Join(pkg.Dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, gosource.Source{Name: path, Data: data})
		}
		return result
	}
	mapped := []gosource.PackageSpec{{Path: "internal/types/errors", Sources: read(pkg.GoFiles)}}
	for _, backend := range []string{"default", "module"} {
		for _, order := range []string{"importer-first", "types-first", "unchanged-root"} {
			t.Run(backend+"/"+order, func(t *testing.T) {
				var fallback types.Importer
				if backend == "module" {
					fallback = lower.NewModuleImporter(pkg.Dir)
				}
				var sources []gosource.Source
				if order == "unchanged-root" {
					sources = read(pkg.XTestGoFiles)
				} else {
					imports := "\"go/importer\"; . \"go/types\""
					if order == "types-first" {
						imports = ". \"go/types\"; \"go/importer\""
					}
					sources = []gosource.Source{{Name: "identity.go", Data: []byte("package errors_test\nimport (" + imports + ")\nvar _ = Config{Importer: importer.Default()}\n")}}
				}
				_, err := gosource.Load(sources, gosource.Options{
					ImportPath: "internal/types/errors_test", Packages: mapped,
					Importer: fallback, PreserveNativeInit: true,
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
