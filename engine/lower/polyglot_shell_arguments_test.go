//go:build full

package lower_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestForeignShellArguments(t *testing.T) {
	fence := `~~~python as py
def number(value: float) -> float: return value
def text(value: str) -> str: return value
~~~
`
	for _, c := range []struct{ name, body, want string }{
		{"shell scalar", "number=2.5\nv := py.number(number)\necho $v\n", "2.5\n"},
		{"shell string", "text='hello world'\nv := py.text(text)\necho $v\n", "hello world\n"},
		{"empty and unset", "text=''\na := py.text(text)\nunset text\nb := py.text(text)\necho \"$a|$b\"\n", "|text\n"},
		{"quoted literal", "text=changed\nv := py.text(\"text\")\necho $v\n", "text\n"},
		{"typed value", "(true)\nnumber := py.number(2)\nv := py.number(number)\necho $v\n", "2\n"},
		{"typed string", "(true)\nvar text string = \"2.5\"\nv := py.number(text)\necho $v\n", "2.5\n"},
		{"shadowing", "text=2.5\nfunc run() {\n var text string = \"3.5\"\n v := py.number(text)\n echo $v\n}\nrun()\nv := py.number(text)\necho $v\n", "3.5\n2.5\n"},
		{"opt in", "number=2.5\nv, err := py.number(number)\necho \"$v|$err\"\n", "2.5|\n"},
		{"invalid", "number=invalid\nv, err := py.number(number)\necho \"$v|$err\"\n", "0|py.number argument 1: strconv.ParseFloat: parsing \"invalid\": invalid syntax\n"},
		{"reassignment", "number=2.5\na := py.number(number)\nnumber=3.5\nb := py.number(number)\necho \"$a|$b\"\n", "2.5|3.5\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			expectForeignOutcomes(t, fence+c.body, foreignOutcome{c.want, ""})
		})
	}
}

// Retain the complete source and argv of the C388 failure, including its shell
// IFS/positional-parameter bridge. Both engines must match the native golden.
func TestForeignShellArgumentsCDiv(t *testing.T) {
	read := func(suffix string) string {
		t.Helper()
		data, err := os.ReadFile("../internal/testdata/c-div/L72." + suffix)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	source, args := read("bsh"), strings.Fields(read("args"))
	want := foreignOutcome{strings.TrimPrefix(read("expected"), "# rc=0\n"), ""}
	// Execute the exact fence body through a native C compiler as well. The
	// appended entry point only supplies the article's unchanged argv bridge.
	body := strings.Split(strings.Split(source, "~~~c as c\n")[1], "~~~")[0]
	dir := t.TempDir()
	file, binary := filepath.Join(dir, "native.c"), filepath.Join(dir, "native")
	if err := os.WriteFile(file, []byte(body+"\nint main(int argc, char **argv) { return example_main(argv[1], atoi(argv[2])); }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("clang", file, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("native C build: %v: %s", err, out)
	}
	if out, err := exec.Command(binary, strings.Join(args, "\x1f"), strconv.Itoa(len(args))).CombinedOutput(); err != nil || string(out) != want.stdout {
		t.Fatalf("native C: %v: %q, want %q", err, out, want.stdout)
	}
	interpreted, compiled := foreignOutcomes(t, source, args...)
	if interpreted != want || compiled != want {
		t.Fatalf("interpreted = %+q; compiled = %+q; want %+q", interpreted, compiled, want)
	}
}
