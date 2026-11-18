//go:build full

package lower

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// This uses the existing injected-provider host contract, but obtains the
// entire program from Compile rather than constructing an entry helper body.
// One compiled module is reused concurrently with independent contexts, shell
// factories, streams, cwd and permission inputs. The host verifies exact
// requests, denied zero-effects behavior and failure/cancellation propagation.
func TestCompiledProviderEntryAcceptance(t *testing.T) {
	const source = `agentic func action() {
 print("native:")
 provider "$LABEL"
}
func main(assist string) {
 if assist == "1" { agentic { action(); } } else { action(); }
}
main("$ASSIST")
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "provider.bpp")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := Compile(file, Options{Package: "generated", Entry: "Execute"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeEntryModule(t, dir)
	generated := filepath.Join(dir, "generated", "program.go")
	host := filepath.Join(dir, "host", "provider_test.go")
	writeEntryFile(t, generated, string(compiled.Source))
	writeEntryFile(t, host, entryProviderHarness)
	binary := filepath.Join(dir, "provider.test")
	runEntryCommand(t, dir, "go", "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	for _, path := range []string{generated, host} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.v", "-test.timeout=15s")
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compiled provider acceptance: %v\n%s", err, output)
	}
}
