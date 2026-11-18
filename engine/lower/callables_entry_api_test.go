package lower_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
)

func TestEntryOptionIsOptIn(t *testing.T) {
	source := "func Execute() { println(7) }\nExecute()\n"
	plain := compile(t, source)
	if plain.Entry != "" || strings.Contains(string(plain.Source), "shellrt") {
		t.Fatal("default native unit acquired a runtime entry")
	}
	result, err := lower.Compile(parse(t, source, "input.bpp"), lower.Options{Entry: "Run"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry != "Run" || !strings.Contains(string(result.Source), "func Run(opts") {
		t.Fatal("missing requested injectable entry")
	}
	execute(t, compiledCase{result, source})
	for _, entry := range []string{"Execute", "lowercase", "not-valid"} {
		result, err := lower.Compile(parse(t, source, "input.bpp"), lower.Options{Entry: entry})
		if result != nil || err == nil {
			t.Fatalf("entry=%q accepted collision/invalid spelling", entry)
		}
	}
}
