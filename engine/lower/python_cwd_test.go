//go:build full

package lower_test

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestPythonFenceTracksShellDirectory(t *testing.T) {
	first, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := "~~~python as py\ndef cwd() -> str:\n    import os\n    return os.getcwd()\n~~~\ncd " + strconv.Quote(first) + "\nfirst := py.cwd()\ncd " + strconv.Quote(second) + "\nsecond := py.cwd()\nprintf '%s|%s\\n' \"$first\" \"$second\"\n"
	expectForeignOutcomes(t, source, foreignOutcome{first + "|" + second + "\n", ""})
}
