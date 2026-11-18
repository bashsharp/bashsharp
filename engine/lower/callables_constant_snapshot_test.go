//go:build full

package lower_test

import "testing"

func TestCompiledConstantSnapshots(t *testing.T) {
	for _, source := range []string{
		"const x int = 1\nfunc worker() {\necho \"$x\"\n}\nfunc main() {\ngo worker()\n}\nmain()\n",
		"const x int = 1\n(echo \"$x\")\necho \"$x\"\n",
		"const x int = 1\n(x=42; echo child)\necho \"$x\"\n",
	} {
		out, diagnostic, status := genericMethodOracle(t, source)
		t.Logf("oracle %q %q %d", out, diagnostic, status)
		execute(t, compile(t, source))
	}
}
