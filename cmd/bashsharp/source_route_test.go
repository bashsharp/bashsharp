package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceRoute(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bashsharp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/bashsharp")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	dir := t.TempDir()
	goBody := "package main\nimport (\"fmt\"; \"os\")\nfunc main(){var in string;fmt.Fscan(os.Stdin,&in);fmt.Fprintln(os.Stderr,\"stderr\",in);fmt.Println(\"compiled\",os.Args[1],in);os.Exit(23)}\n"
	for name, body := range map[string]string{"x.go": goBody, "x.txt": goBody, "x": goBody, "x.bsh": "package main\nfunc main(){println(\"interpreted\")}\n", "interpret.go": "echo interpreted\n", "harness.go": "package main\nfunc main(){println(\"interpreted\")}\n", "x.c": "#include <stdio.h>\nint main(){fputs(\"c\", stdout);}\n", "x.cc": "#include <iostream>\nint main(){std::cout << \"cc\";}\n", "x.cpp": "#include <iostream>\nint main(){std::cout << \"cpp\";}\n", "x.cxx": "#include <iostream>\nint main(){std::cout << \"cxx\";}\n", "x.ts": "console.log('typescript')\n", "x.tsx": "console.log('tsx')\n", "x.js": "console.log('javascript', process.argv[2])\n", "x.mjs": "console.log('mjs')\n", "x.py": "import sys; print('python', sys.argv[1])\n", "x.rs": "fn main(){print!(\"rust\");}\n", "x.fs": "printfn \"fsharp\"\n", "x.fsx": "printfn \"fsharp-script\"\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	moduleFiles := map[string]string{
		"module/go.mod":          "module example.test/route\n\ngo 1.27\n",
		"module/value/value.go":  "package value\nconst Text = \"module\"\n",
		"module/cmd/app/main.go": "package main\nimport (\"fmt\"; \"example.test/route/value\")\nfunc main(){fmt.Print(value.Text)}\n",
		"library.go":             "package library\nfunc Exported() int { return 1 }\n",
	}
	for name, body := range moduleFiles {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The route runs Rust only where a compiler exists; elsewhere it must say so.
	rustOut, rustCode := "Rust compiler unavailable", 2
	if _, err := exec.LookPath("rustc"); err == nil {
		rustOut, rustCode = "rust", 0
	}
	for _, tc := range []struct {
		name   string
		args   []string
		output string
		code   int
	}{
		{"bsh Go content", []string{"x.bsh"}, "interpreted", 0},
		{"bare go", []string{"x.go", "arg"}, "compiled arg in", 23},
		{"go flag no op", []string{"--source=go", "x.go", "arg"}, "compiled arg in", 23},
		{"go flag txt", []string{"--source=go", "x.txt", "arg"}, "compiled arg in", 23},
		{"go flag no extension", []string{"--source=go", "x", "arg"}, "compiled arg in", 23},
		{"go module directory", []string{"--source=go", "module/cmd/app"}, "module", 0},
		{"non-main refusal", []string{"library.go"}, "library.go:1:1: Go source package library cannot run as a program; expose its exported functions from a ~~~go fence", 2},
		{"bashsharp override", []string{"--bashsharp", "interpret.go"}, "interpreted", 0},
		{"harness override", []string{"--bashpp", "--source=go", "harness.go"}, "interpreted", 0},
		{"c extension", []string{"x.c"}, "c", 0},
		{"cc extension", []string{"x.cc"}, "cc", 0},
		{"cpp extension", []string{"x.cpp"}, "cpp", 0},
		{"cxx extension", []string{"x.cxx"}, "cxx", 0},
		{"typescript extension", []string{"x.ts"}, "typescript", 0},
		{"tsx extension", []string{"x.tsx"}, "tsx", 0},
		{"javascript extension", []string{"x.js", "arg"}, "javascript arg", 0},
		{"module javascript extension", []string{"x.mjs"}, "mjs", 0},
		{"python extension", []string{"x.py", "arg"}, "python arg", 0},
		// Source-route coverage is host-independent: without a Rust compiler it
		// must give the actionable fence diagnostic. Rust execution itself is
		// exercised by the provisioned polyglot gate.
		{"rust extension", []string{"x.rs"}, rustOut, rustCode},
		{"flag beats python extension", []string{"--source=go", "x.py"}, "expected 'package'", 1},
		// F# source files are deliberately not one of the shipped whole-file
		// runners. Keep the binary's route aligned with polyglot.RunSourceFile:
		// it must name the unsupported extension rather than attempting shell
		// interpretation or claiming an unavailable runner succeeded.
		{"fsharp extension refusal", []string{"x.fs"}, "unsupported source extension .fs", 2},
		{"fsharp script extension refusal", []string{"x.fsx"}, "unsupported source extension .fsx", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Dir = dir
			// The fixture creates its own module below dir. A parent go.work must
			// not turn that independent module into a workspace-membership error.
			cmd.Env = append(os.Environ(), "GOWORK=off")
			cmd.Stdin = strings.NewReader("in\n")
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					code = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.code || !strings.Contains(string(out), tc.output) {
				t.Fatalf("code=%d output=%q; want %d %q", code, out, tc.code, tc.output)
			}
			if tc.code == 23 && !strings.Contains(string(out), "stderr in") {
				t.Fatalf("stderr not passed through: %q", out)
			}
		})
	}
}
