package gosource

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"os"
	"path/filepath"
	"testing"
)

// A companion's bytes are part of the prepared program: changing them must
// change the key, and a companion the key cannot read must disable the cache.
func TestPreparedProgramKeyAuthenticatesCompanionContents(t *testing.T) {
	dir := t.TempDir()
	asm := filepath.Join(dir, "add_amd64.s")
	if err := os.WriteFile(asm, []byte("TEXT ·add(SB),0,$0\n\tRET\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{RunMain: true, Packages: []PackageSpec{{Path: "example/add", SourceDir: dir, CompanionFiles: []string{"add_amd64.s"}}}}
	first, ok := preparedProgramKey("interp", nil, options)
	if !ok {
		t.Fatal("key refused a readable companion")
	}
	if err := os.WriteFile(asm, []byte("TEXT ·add(SB),0,$0\n\tNOP\n\tRET\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, ok := preparedProgramKey("interp", nil, options)
	if !ok {
		t.Fatal("key refused a readable companion")
	}
	if first == second {
		t.Fatal("changing a companion's bytes did not change the prepared-program key")
	}
	if err := os.Remove(asm); err != nil {
		t.Fatal(err)
	}
	if _, ok := preparedProgramKey("interp", nil, options); ok {
		t.Fatal("an unreadable companion must disable the prepared-program cache")
	}
}
