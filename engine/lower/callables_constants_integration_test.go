//go:build full

package lower_test

import "testing"

func TestCompiledConstantDeclarationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, source, out, diagnostic string
		status                        int
	}{
		{"root_order", "echo \"${x-unset}\"\nconst x int = 1\necho \"$x\"\n", "unset\n1\n", "", 0},
		{"root_exit", "const x int = 1\nexit 7\necho after\n", "", "", 7},
		{"shadow", "const x int = 1\nfunc show() {\nconst x int = 2\necho \"$x\"\n}\nshow()\necho \"$x\"\n", "2\n1\n", "", 0},
		{"local_assign", "func main() {\nconst x int = 1\nx=42\necho after\n}\nmain()\n", "", "x: cannot assign to const\n", 1},
		{"root_assign", "const x int = 1\nx=42\necho after\n", "after\n", "x: cannot assign to const\n", 0},
		{"local_unset", "func main() {\nconst x int = 1\nunset 'x'\necho \"shell=$x status=$?\"\nprintln(x)\n}\nmain()\n", "shell=1 status=1\n1\n", "unset: x: cannot unset: readonly variable\n", 0},
		{"root_capture", "const x int = 1\nfunc show() {\necho \"$x\"\n}\nshow()\nshow()\n", "1\n1\n", "", 0},
		{"repeated_local", "func show() {\nconst x int = 1\necho \"$x\"\n}\nshow()\nshow()\n", "1\n1\n", "", 0},
		{"root_group", "const (\nx int = 1\ny int = 2\n)\necho \"$x:$y\"\nx=42\necho after\n", "1:2\nafter\n", "x: cannot assign to const\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, status := genericMethodOracle(t, tc.source)
			if out != tc.out || diagnostic != tc.diagnostic || status != tc.status {
				t.Fatalf("source oracle: %q %q %d", out, diagnostic, status)
			}
			execute(t, compile(t, tc.source))
		})
	}
}
