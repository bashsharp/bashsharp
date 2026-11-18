package polyglot

import (
	"context"
	"testing"
)

func TestLanguageTable(t *testing.T) {
	for spelling, canonical := range map[string]string{
		"python": "python", "py": "python", "PY": "python",
		"typescript": "typescript", "ts": "typescript",
		"javascript": "javascript", "js": "javascript", "JS": "javascript",
		"rust": "rust", "rs": "rust",
		"c": "c", "cpp": "cpp", "cxx": "cpp",
		"go":         "go",
		"powershell": "powershell", "pwsh": "powershell", "ps1": "powershell", "PWSH": "powershell",
		"csharp": "csharp", "cs": "csharp", "CS": "csharp",
	} {
		if got := CanonicalLanguage(spelling); got != canonical {
			t.Errorf("CanonicalLanguage(%q) = %q, want %q", spelling, got, canonical)
		}
		row, ok := LookupLanguage(spelling)
		if !ok || row.Canonical != canonical {
			t.Errorf("LookupLanguage(%q) = %+v, %v", spelling, row, ok)
		}
	}
	// An unregistered spelling passes through so Prepare can name it.
	if got := CanonicalLanguage("yaml"); got != "yaml" {
		t.Errorf("CanonicalLanguage(yaml) = %q", got)
	}
	if _, ok := LookupLanguage("yaml"); ok {
		t.Errorf("yaml has a row")
	}
	if _, err := Prepare(context.Background(), []Block{{Language: "yaml", Source: "a: 1"}}, map[string]Analyzer{}); err == nil || err.Error() != `polyglot: unsupported language "yaml"` {
		t.Errorf("Prepare(yaml) = %v", err)
	}
	// Both Windows-familiar fences lower: the binary drives the same pinned
	// pwsh worker the interpreter does.
	for _, language := range []string{"powershell", "csharp"} {
		row, _ := LookupLanguage(language)
		if row.LoweredRuntime == nil || row.LoweredRuntime("x.", "nil") == "" {
			t.Errorf("row %s has no lowered runtime", language)
		}
	}
	for _, row := range Languages() {
		if row.NewRuntime == nil || row.LoweredRuntime == nil {
			t.Errorf("row %s incomplete: %+v", row.Canonical, row)
		}
		if got := row.LoweredRuntime("x.", "nil"); got == "" {
			t.Errorf("row %s lowered runtime empty", row.Canonical)
		}
	}
	if !languageFlag("cxx", func(l Language) bool { return l.LineDirectives }) || languageFlag("go", func(l Language) bool { return l.LineDirectives }) {
		t.Errorf("LineDirectives flags wrong")
	}
	if !languageFlag("rs", func(l Language) bool { return l.Callbacks }) {
		t.Errorf("rust must carry Callbacks")
	}
}

func TestRegisterLanguageReplacesRow(t *testing.T) {
	embedded := Embedded{RuntimeName: "fake", AnalyzeFunc: func(context.Context, string) ([]Export, error) {
		return []Export{{Name: "hello"}}, nil
	}}
	RegisterLanguage(Language{Canonical: "fake", Aliases: []string{"fk"},
		NewRuntime:     func(RuntimeConfig) LanguageRuntime { return embedded },
		LoweredRuntime: func(prefix, _ string) string { return prefix + "fake" }})
	t.Cleanup(func() {
		languagesMu.Lock()
		delete(languages, "fake")
		delete(languageAliases, "fk")
		delete(languageAliases, "fake2")
		languagesMu.Unlock()
	})
	if got := CanonicalLanguage("fk"); got != "fake" {
		t.Fatalf("alias fk → %q", got)
	}
	row, _ := LookupLanguage("fk")
	plans, err := Prepare(context.Background(), []Block{{Language: "fk", Alias: "f", Source: "x"}}, map[string]Analyzer{"fake": row.NewRuntime(RuntimeConfig{})})
	if err != nil || len(plans) != 1 || plans[0].Language != "fake" || len(plans[0].Exports) != 1 || plans[0].Exports[0].Name != "hello" {
		t.Fatalf("Prepare through a registered row: %+v, %v", plans, err)
	}
	// Re-registering the canonical name replaces the row and retires the
	// old aliases.
	RegisterLanguage(Language{Canonical: "fake", Aliases: []string{"fake2"},
		NewRuntime: func(RuntimeConfig) LanguageRuntime { return embedded }})
	if _, ok := LookupLanguage("fk"); ok {
		t.Errorf("stale alias fk survived re-registration")
	}
	if got := CanonicalLanguage("fake2"); got != "fake" {
		t.Errorf("new alias fake2 → %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Errorf("RegisterLanguage without NewRuntime did not panic")
		}
	}()
	RegisterLanguage(Language{Canonical: "broken"})
}
