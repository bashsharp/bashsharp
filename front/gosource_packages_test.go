// Sprint: #149; Story: S149.12; Story-ID: e9c799a66ea7
package front

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStripGoSourcePackageFlags(t *testing.T) {
	got, sel, err := StripGoSourceInvocationFlags([]string{"bashy", "--source=go", "--go-list",
		"--go-package", "test/a=a.go", "--go-package=test/b=b.go,b2.go", "--go-package-asm", "test/a=a.s", "--go-package-asm=test/a=b.s", "--go-import-base", "test", "--go-import-path=test/c", "--go-file", "c.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"bashy"}) {
		t.Errorf("args = %q", got)
	}
	if !sel.List || sel.ImportBase != "test" || sel.ImportPath != "test/c" || len(sel.Packages) != 2 ||
		sel.Packages[0].Path != "test/a" || !slices.Equal(sel.Packages[0].Files, []string{"a.go"}) ||
		sel.Packages[1].Path != "test/b" || !slices.Equal(sel.Packages[1].Files, []string{"b.go", "b2.go"}) ||
		len(sel.PackageAssemblies) != 2 || sel.PackageAssemblies[0] != (GoSourcePackageAssemblySpec{Path: "test/a", File: "a.s"}) || sel.PackageAssemblies[1] != (GoSourcePackageAssemblySpec{Path: "test/a", File: "b.s"}) {
		t.Errorf("selection = %+v", sel)
	}
	for _, bad := range [][]string{
		{"bashy", "--source=go", "--go-package"},
		{"bashy", "--source=go", "--go-package", "nofiles"},
		{"bashy", "--source=go", "--go-package", "=a.go"},
		{"bashy", "--source=go", "--go-package", "p=a.go,"},
		{"bashy", "--source=go", "--go-package-asm"},
		{"bashy", "--source=go", "--go-package-asm", "p=a.go"},
		{"bashy", "--source=go", "--go-package-asm", "p=a.s,b.s"},
		{"bashy", "--source=go", "--go-import-base"},
		{"bashy", "--source=go", "--go-import-base="},
	} {
		if _, _, err := StripGoSourceInvocationFlags(bad); err == nil {
			t.Errorf("%q: want a usage error", bad)
		}
	}
}

func TestGoSourcePackageAssemblyIsQualifiedAndSameDirectory(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "p.go")
	assembly := filepath.Join(dir, "p.s")
	if err := os.WriteFile(source, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assembly, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, sel, err := StripGoSourceInvocationFlags([]string{"bashy", "--source=go", "--go-package-asm=p=" + assembly, "--go-package=p=" + source})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ResolveGoSource(sel, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true})
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := ReadGoSourcePackages(res.Packages)
	if err != nil || len(pkgs) != 1 || pkgs[0].SourceDir != dir || !slices.Equal(pkgs[0].CompanionFiles, []string{assembly}) {
		t.Fatalf("packages = %+v, %v", pkgs, err)
	}

	_, sel, err = StripGoSourceInvocationFlags([]string{"bashy", "--source=go", "--go-package-asm=q=" + assembly, "--go-package=p=" + source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveGoSource(sel, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true}); err == nil {
		t.Fatal("unqualified assembly companion was accepted")
	}
	other := filepath.Join(t.TempDir(), "p.s")
	if _, err := ReadGoSourcePackages([]GoSourcePackageSpec{{Path: "p", Files: []string{source}, CompanionFiles: []string{other}}}); err == nil {
		t.Fatal("cross-directory assembly companion was accepted")
	}
}

func TestResolveGoSourcePackageRefusals(t *testing.T) {
	bashy := GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true}
	pkg := []GoSourcePackageSpec{{Path: "test/a", Files: []string{"a.go"}}}
	tests := []struct {
		name string
		sel  GoSourceSelection
		want string
	}{
		{"package without go", GoSourceSelection{Packages: pkg}, "bashy: --go-package requires --source=go"},
		{"assembly without go", GoSourceSelection{PackageAssemblies: []GoSourcePackageAssemblySpec{{Path: "test/a", File: "a.s"}}}, "bashy: --go-package-asm requires --source=go"},
		{"base without go", GoSourceSelection{ImportBase: "test"}, "bashy: --go-import-base requires --source=go"},
		{"list without go", GoSourceSelection{List: true}, "bashy: --go-list requires --source=go"},
		{"path without go", GoSourceSelection{ImportPath: "test/b"}, "bashy: --go-import-path requires --source=go"},
		{"test-main without go", GoSourceSelection{TestMain: true}, "bashy: --go-test-main requires --source=go"},
		// Sprint 165 D8: the test-main FACT asserts the program's identity, so
		// it is refused without one — never inferred from a ".test" suffix.
		{"test-main without path", GoSourceSelection{Language: "go", LanguageSeen: true, TestMain: true}, "bashy: --go-test-main asserts the identity of the program and requires --go-import-path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveGoSource(tc.sel, bashy)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	res, err := ResolveGoSource(GoSourceSelection{Language: "go", LanguageSeen: true, List: true, Packages: pkg, ImportBase: "test"}, bashy)
	if err != nil || !res.Check || !res.List || res.ImportBase != "test" || len(res.Packages) != 1 {
		t.Fatalf("resolution = %+v, %v; --go-list must imply --check and carry the map", res, err)
	}
	res, err = ResolveGoSource(GoSourceSelection{Language: "go", LanguageSeen: true, ImportPath: "cmd/x.test", TestMain: true}, bashy)
	if err != nil || res.ImportPath != "cmd/x.test" || !res.TestMain {
		t.Fatalf("resolution = %+v, %v; --go-test-main with --go-import-path must carry both", res, err)
	}
}

func TestWriteGoSourceListIsOneJSONObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteGoSourceList(&buf, []GoSourceImportResolution{
		{From: "test/b", Import: "./a", Path: "test/a", Origin: "package-map", Name: "a", Files: []string{"a.go"}},
		{From: "main", Import: "fmt", Path: "fmt", Origin: "importer", Name: "fmt"},
	}); err != nil {
		t.Fatal(err)
	}
	want := `{"from":"test/b","import":"./a","path":"test/a","origin":"package-map","name":"a","files":["a.go"]}` + "\n" +
		`{"from":"main","import":"fmt","path":"fmt","origin":"importer","name":"fmt"}` + "\n"
	if buf.String() != want {
		t.Fatalf("list =\n%s\nwant\n%s", buf.String(), want)
	}
	if strings.Count(buf.String(), "\n") != 2 {
		t.Fatal("want exactly one line per resolution")
	}
}
