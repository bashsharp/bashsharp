package lower

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func TestProgramMainClosesForeignWorkersBeforeExit(t *testing.T) {
	e := &emitter{
		prefix:         "cleanup_",
		foreignImports: []polyglot.ImportPlan{{}, {}},
		foreignPlans:   []polyglot.Plan{{}, {}},
	}
	source := e.programEntrySource("", false)

	ordered := []string{
		"func cleanup_mainEntry() (status int, err error)",
		"_ = cleanup_foreign1.Close()",
		"_ = cleanup_foreign0.Close()",
		"_ = cleanup_python1.Close()",
		"_ = cleanup_python0.Close()",
		"return cleanup_execute()",
		"status,err := cleanup_mainEntry()",
		"cleanup_os.Exit(status)",
	}
	last := -1
	for _, text := range ordered {
		at := strings.Index(source, text)
		if at < 0 {
			t.Fatalf("generated entry omitted %q:\n%s", text, source)
		}
		if at <= last {
			t.Fatalf("generated entry ordered %q before its predecessor:\n%s", text, source)
		}
		last = at
	}
	if strings.Count(source, ".Close()") != 4 {
		t.Fatalf("generated entry close count = %d, want 4:\n%s", strings.Count(source, ".Close()"), source)
	}
}

func TestProgramMainWithoutForeignWorkersIsUnchanged(t *testing.T) {
	source := (&emitter{prefix: "plain_"}).programEntrySource("", false)
	if strings.Contains(source, "mainEntry") || strings.Contains(source, ".Close()") {
		t.Fatalf("plain generated entry gained foreign cleanup:\n%s", source)
	}
	if !strings.Contains(source, "status,err := plain_execute()") {
		t.Fatalf("plain generated entry lost its direct call:\n%s", source)
	}
}

func TestFoundationMainClosesForeignWorkersBeforeExit(t *testing.T) {
	e := &emitter{
		prefix:         "cleanup_",
		foreignImports: []polyglot.ImportPlan{{}, {}},
		foreignPlans:   []polyglot.Plan{{}, {}},
	}
	source := e.foundationMain("panic-boundary\n", "body\n", "cleanup_rt.Exit()\n")

	ordered := []string{
		"func cleanup_mainEntry()",
		"panic-boundary",
		"defer func()",
		"_ = cleanup_foreign1.Close()",
		"_ = cleanup_foreign0.Close()",
		"_ = cleanup_python1.Close()",
		"_ = cleanup_python0.Close()",
		"body",
		"func main()",
		"\ncleanup_mainEntry()\n",
		"cleanup_rt.Exit()",
	}
	last := -1
	for _, text := range ordered {
		at := strings.Index(source, text)
		if at < 0 {
			t.Fatalf("generated foundation main omitted %q:\n%s", text, source)
		}
		if at <= last {
			t.Fatalf("generated foundation main ordered %q before its predecessor:\n%s", text, source)
		}
		last = at
	}
}

func TestFoundationMainWithoutForeignWorkersIsUnchanged(t *testing.T) {
	source := (&emitter{prefix: "plain_"}).foundationMain("head\n", "body\n", "tail\n")
	if want := "func main() {\nhead\nbody\ntail\n}\n"; source != want {
		t.Fatalf("plain foundation main changed:\n%s\nwant:\n%s", source, want)
	}
}
