package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReproducers keeps the out-of-corpus reproducers honest: every
// reject.go is rejected by the Bash++ front end and every accept.go is
// accepted. It never keys on a message — the wording is the finding, not
// the gate (see testdata/reproducers/README.md).
func TestReproducers(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("testdata", "reproducers", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no reproducers found")
	}
	for _, dir := range dirs {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		for _, name := range []string{"reject.go", "accept.go"} {
			path := filepath.Join(dir, name)
			if name == "reject.go" {
				// Deliberately invalid Go lives as reject.go.src so the
				// repository-wide gofmt gate does not parse it; it is fed to
				// the front end under the name reject.go.
				path += ".src"
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			diags, _ := FrontEnd(RootSpec{Root: path, Short: name, Src: src})
			if name == "accept.go" && len(diags) > 0 {
				t.Errorf("%s: accepted form rejected: %v", path, diags[0].Raw)
			}
			if name == "reject.go" && len(diags) == 0 {
				t.Errorf("%s: rejected form accepted", path)
			}
			t.Logf("%s: %d diagnostics", path, len(diags))
			for _, d := range diags {
				t.Logf("    [%s] %s", d.Stage, d.Raw)
			}
		}
	}
}
