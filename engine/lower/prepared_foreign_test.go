//go:build full

package lower_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestOtherLanguagePreparationRemainsFresh(t *testing.T) {
	const language = "lowerprepareprobe"
	count := 0
	var onAnalyze func()
	register := func(name string) {
		polyglot.RegisterLanguage(polyglot.Language{
			Canonical: language,
			NewRuntime: func(polyglot.RuntimeConfig) polyglot.LanguageRuntime {
				return polyglot.Embedded{RuntimeName: language, AnalyzeFunc: func(_ context.Context, source string) ([]polyglot.Export, error) {
					count++
					if strings.Contains(source, "invalid body") {
						return nil, errors.New("invalid body")
					}
					if onAnalyze != nil {
						onAnalyze()
					}
					return []polyglot.Export{{Name: name}}, nil
				}}
			},
			LoweredRuntime: func(string, string) string { return "nil" },
		})
	}
	compile := func(source string) error {
		t.Helper()
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "probe.bpp")
		if err != nil {
			return err
		}
		_, err = lower.Compile(file, lower.Options{Origin: "probe.bpp"})
		return err
	}
	register("first")
	source := "~~~" + language + " as p\nbody\n~~~\nx := 1\n"
	if err := compile(source); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("one Compile with inference prepared %d times, want 2", count)
	}
	if err := compile(source); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("separate Compile reused preparation: count %d", count)
	}
	register("second")
	if err := compile(source); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("provider replacement reused preparation: count %d", count)
	}
	if err := compile(strings.Replace(source, "body", "invalid body", 1)); err == nil || !strings.Contains(err.Error(), "invalid body") {
		t.Fatalf("invalid source diagnostic = %v", err)
	}
	if count != 7 {
		t.Fatalf("invalid source reused preparation: count %d", count)
	}
	onAnalyze = func() {
		onAnalyze = nil
		register("changedDuringCompile")
	}
	register("initialDuringCompile")
	if err := compile(source); err != nil {
		t.Fatal(err)
	}
	if count != 9 {
		t.Fatalf("provider change during inference prepared %d times total, want 9", count)
	}
}
