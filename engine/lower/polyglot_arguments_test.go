//go:build full

package lower_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestForeignScalarArguments(t *testing.T) {
	source, err := os.ReadFile("../internal/testdata/python-scalars/scalars.bsh")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../internal/testdata/python-scalars/scalars.expected")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimPrefix(string(golden), "# rc=0\n")
	// The original corpus source and golden are copied byte-for-byte.
	for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
		t.Run("corpus/"+mode, func(t *testing.T) {
			expectForeignOutcomes(t, prelude+string(source), foreignOutcome{want, ""})
		})
	}
	fence := `~~~python as py
def as_float(value: float) -> float:
    print("called")
    return value
def as_int(value: int) -> int: return value
def as_bool(value: bool) -> bool: return value
~~~
`
	cases := []struct{ name, body, out, err string }{
		{"float value", "x := py.as_float(\"2.5\")\nv := py.as_float(x)\necho $v\n", "called\ncalled\n2.5\n", ""},
		{"numeric strings", "i := py.as_int(\"0x10\")\nb := py.as_bool(\"true\")\necho $i $b\n", "16 true\n", ""},
		{"invalid opt in", "v, err := py.as_float(\"invalid\")\necho \"$v|$err|$?\"\n", "0|py.as_float argument 1: strconv.ParseFloat: parsing \"invalid\": invalid syntax|0\n", ""},
		{"invalid ordinary", "py.as_float(\"invalid\")\necho status=$?\n", "status=1\n", "bash++: foreign call py.as_float failed: py.as_float argument 1: strconv.ParseFloat: parsing \"invalid\": invalid syntax\n"},
	}
	for _, c := range cases {
		for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				expectForeignOutcomes(t, fence+prelude+c.body, foreignOutcome{c.out, c.err})
			})
		}
	}
	t.Run("native Python", func(t *testing.T) {
		// Python annotations alone do not coerce. Model the existing shell boundary
		// explicitly, then execute the unmodified definitions from the corpus.
		body := strings.Split(strings.Split(string(source), "~~~py as py\n")[1], "~~~")[0]
		script := body + `import json
print(str(as_int(int("7", 0)))+"|"+str(as_float(float("2.5")))+"|"+as_str("hello")+"|"+str(as_bool(True)).lower()+"|"+as_bytes().decode()+"|"+("" if as_none() is None else "bad")+"|"+json.dumps(as_any(),separators=(",",":")))
`
		out, err := exec.Command("python3", "-c", script).CombinedOutput()
		if err != nil || string(out) != want {
			t.Fatalf("native: %v %q, want %q", err, out, want)
		}
	})
}

// Exercise the generated Go boundary with an effectful expression. This
// isolates argument capture from the parser's restrictions on nested calls.
func TestForeignScalarArgumentSingleEvaluation(t *testing.T) {
	for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
		t.Run(mode, func(t *testing.T) {
			setup, argument := "", ""
			if mode == "execution" {
				setup = "program, problem := __bpp0_rt.NewProgram(); if problem != nil { panic(problem) }"
				argument = "program, "
			}
			postlude := `
func init() {
 SETUP
 count := 0
 next := func() string { count++; return "2.5" }
 if got := __bpp0_foreign0_call_as_float(ARGnext()); got != 2.5 || count != 1 { panic("argument evaluated more than once") }
 bad := func() string { count++; return "invalid" }
 if got, err := __bpp0_foreignErr0_as_float(ARGbad()); got != 0 || err == nil || count != 2 { panic("invalid argument capture") }
}
`
			postlude = strings.ReplaceAll(strings.ReplaceAll(postlude, "SETUP", setup), "ARG", argument)
			if mode == "execution" {
				postlude += `
type scalarDiscard struct{}
func (scalarDiscard) Write(b []byte) (int,error) { return len(b),nil }
func init() {
 ctx, cancel := __bpp0_context.WithCancel(__bpp0_context.Background())
 cancel()
 program, problem := __bpp0_rt.NewProgram(__bpp0_rt.WithContext(ctx))
 if problem != nil { panic(problem) }
 previous := __bpp0_rt.Stderr
 __bpp0_rt.Stderr = scalarDiscard{}
 defer func(){ __bpp0_rt.Stderr = previous }()
 value, callErr := __bpp0_foreignErr0_as_float(program,"2.5")
 if value != 0 || callErr != nil || program.Status() == 0 { panic("cancellation must remain infrastructure failure") }
}
`
			}
			testForeignParityArtifactAt(t, `~~~python as py
def as_float(value: float) -> float: return value
~~~
`+prelude+`v := py.as_float("2.5")
echo $v
`, "input.bpp", postlude)
		})
	}
}

func TestForeignBytesResults(t *testing.T) {
	fence := `~~~python as py
def raw() -> bytes: return bytes([255, 128, 120])
def empty() -> bytes: return b""
def absent() -> bytes: raise ValueError("missing")
def size(value: bytes) -> int: return len(value)
~~~
`
	for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
		t.Run(mode, func(t *testing.T) {
			expectForeignOutcomes(t, fence+prelude+`b := py.raw()
e := py.empty()
n, err := py.absent()
size := py.size(b)
echo "$b|$e|$n|$size"
`, foreignOutcome{"\xff\x80x|||3\n", ""})
		})
	}
}

// These assignments and higher-order calls compiled before scalar coercion was
// added. Keep the public function-value contract independent of direct ingress.
func TestForeignScalarFunctionValues(t *testing.T) {
	for mode, prelude := range map[string]string{"static": "", "execution": "(true)\n"} {
		t.Run(mode, func(t *testing.T) {
			signature, args, setup := "float64", "2.5", ""
			if mode == "execution" {
				signature = "*__bpp0_foreignProgramState,float64"
				args = "program,2.5"
				setup = "program, err := __bpp0_rt.NewProgram(); if err != nil { panic(err) }"
			}
			postlude := `
func init() {
 SETUP
 var f func(SIGNATURE) float64 = py.as_float
 pass := func(value func(SIGNATURE) float64) float64 { return value(ARGS) }
 if f(ARGS) != 2.5 || pass(py.as_float) != 2.5 { panic("typed foreign function value") }
}
`
			postlude = strings.NewReplacer("SETUP", setup, "SIGNATURE", signature, "ARGS", args).Replace(postlude)
			testForeignParityArtifactAt(t, `~~~python as py
def as_float(value: float) -> float: return value
~~~
`+prelude+`v := py.as_float(2)
echo $v
`, "input.bpp", postlude)
		})
	}
}
