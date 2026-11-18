package polyglot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextRowMaterializesAndRunsTool(t *testing.T) {
	saved := ToolResolver
	ToolResolver = func(name string) ([]string, string, error) {
		if name != "fake-tool" {
			t.Fatalf("resolved %q", name)
		}
		return []string{"/bin/sh", "-c", `printf '%s|%s|%s|%s|%s\n' "$0" "$(basename "$1")" "$(cat "$1" | tr -d '\n')" "$2" "$BASHPP_FENCE_TYPE"`}, "fake", nil
	}
	t.Cleanup(func() { ToolResolver = saved })
	text := Text{Type: "fakecfg", FileName: "fake.cfg", Tool: "fake-tool", Verbs: []Verb{
		{Name: "show", Args: []string{"show", "{file}"}},
		{Name: "apply", Args: []string{"apply", "{file}"}, Effects: []string{"write", "net"}},
	}}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "fakecfg", Alias: "c", Source: "k=v\n"}}, map[string]Analyzer{"fakecfg": text})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	if len(plan.Exports) != 2 || len(plan.Exports[1].Effects) != 2 || plan.Exports[1].Effects[1] != "net" || !plan.Exports[0].Signature.Variadic {
		t.Fatalf("exports = %+v", plan.Exports)
	}
	module := Start(plan, text)
	result, err := module.Call(ctx, "show", "extra")
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != "show|fake.cfg|k=v|extra|fakecfg" {
		t.Fatalf("value = %q", result.Value)
	}
	file := filepath.Join(textRoot(), plan.ID, "fake.cfg")
	if data, err := os.ReadFile(file); err != nil || string(data) != "k=v\n" {
		t.Fatalf("materialized %s: %q, %v", file, data, err)
	}
	if _, err := module.Call(ctx, "destroy"); err == nil || !strings.Contains(err.Error(), "no method destroy") {
		t.Fatalf("undeclared verb: %v", err)
	}
	// A failing tool is the call's error, with stderr carried.
	ToolResolver = func(string) ([]string, string, error) {
		return []string{"/bin/sh", "-c", "echo boom >&2; exit 3"}, "", nil
	}
	if result, err := module.Call(ctx, "apply"); err == nil || !strings.Contains(err.Error(), "exited 3") || result.Stderr != "boom\n" {
		t.Fatalf("failing tool: %v %+v", err, result)
	}
}

func TestTextVerbNormalizesCRLFValue(t *testing.T) {
	saved := ToolResolver
	ToolResolver = func(string) ([]string, string, error) {
		return []string{os.Args[0], "-test.run=^TestTextVerbCRLFHelper$"}, "helper", nil
	}
	t.Cleanup(func() { ToolResolver = saved })
	text := Text{
		Type: "crlf", FileName: "fence.crlf", Tool: "helper",
		Environ: append(os.Environ(), "BASHPP_TEXT_CRLF_HELPER=1"),
		Verbs:   []Verb{{Name: "show"}},
	}
	plans, err := Prepare(t.Context(), []Block{{Language: "crlf", Alias: "c", Source: "sample\n"}}, map[string]Analyzer{"crlf": text})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Start(plans[0], text).Call(t.Context(), "show")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "first\nsecond\nthird\rfourth" || got.Stderr != "warning\r\n" {
		t.Fatalf("text value or raw stderr changed: %+v", got)
	}
}

func TestTextVerbCRLFHelper(t *testing.T) {
	if os.Getenv("BASHPP_TEXT_CRLF_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte("first\r\nsecond\r\nthird\rfourth\r\n"))
	_, _ = os.Stderr.Write([]byte("warning\r\n"))
	os.Exit(0)
}

func TestNativeTextProcessorEnvKeepsShellProfileSeparate(t *testing.T) {
	shell := []string{"USERPROFILE=/c/Users/agent", "TEMP=/c/Users/agent/AppData/Local/Temp", "TMP=/c/tmp", "HOME=/c/Users/agent", "CUSTOM=/c/data"}
	windows := nativeTextProcessorEnv(shell, true)
	if windows[0] != `USERPROFILE=C:\Users\agent` || windows[1] != `TEMP=C:\Users\agent\AppData\Local\Temp` || windows[2] != `TMP=C:\tmp` || windows[3] != shell[3] || windows[4] != shell[4] {
		t.Fatalf("native Windows processor env = %#v", windows)
	}
	if shell[0] != "USERPROFILE=/c/Users/agent" {
		t.Fatalf("shell environment mutated: %#v", shell)
	}
	other := nativeTextProcessorEnv(shell, false)
	if other[0] != shell[0] {
		t.Fatalf("non-Windows environment changed: %#v", other)
	}
}

func TestRunnerFenceDeclaresMethods(t *testing.T) {
	var seen [][]string
	fence := RunnerFence{Type: "notes", Runner: "tally", Invoke: func(_ context.Context, argv []string) (string, error) {
		seen = append(seen, argv)
		switch argv[0] {
		case MethodsVerb:
			return "{\"name\":\"count\"}\n\n{\"name\":\"apply\",\"effect\":\"write, net\",\"signature\":{\"params\":[\"string\"],\"results\":[\"string\"]}}\n", nil
		case "count":
			data, err := os.ReadFile(argv[1])
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(strings.Repeat("x", strings.Count(string(data), "\n"))), nil
		}
		return "", nil
	}}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "notes", Alias: "n", Runner: "tally", Source: "a\nb\n"}}, map[string]Analyzer{"notes": fence})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	if plan.Runner != "tally" || len(plan.Exports) != 2 || strings.Join(plan.Exports[1].Effects, "+") != "write+net" || plan.Exports[1].Signature.Variadic {
		t.Fatalf("plan = %+v", plan)
	}
	module := Start(plan, fence)
	result, err := module.Call(ctx, "count")
	if err != nil || result.Value != "xx" {
		t.Fatalf("count = %v, %v", result, err)
	}
	if _, err := module.Call(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "declares no method nope") {
		t.Fatalf("undeclared: %v", err)
	}
	if seen[0][0] != MethodsVerb || filepath.Base(seen[0][1]) != "fence.notes" || filepath.Base(seen[1][1]) != "fence.notes" {
		t.Fatalf("argv seen = %v", seen)
	}
	// Runner and alias must agree across blocks of one type.
	if _, err := Prepare(ctx, []Block{{Language: "notes", Alias: "n", Runner: "tally"}, {Language: "notes", Alias: "n", Runner: "other"}}, map[string]Analyzer{"notes": fence}); err == nil || !strings.Contains(err.Error(), "inconsistent runners") {
		t.Fatalf("inconsistent runners: %v", err)
	}
}

func TestParseMethods(t *testing.T) {
	for answer, want := range map[string]string{
		"":                                   "declared no methods",
		"\n  \n":                             "declared no methods",
		"not json":                           "not a method object",
		`{"signature":{}}`:                   "without a name",
		`{"name":"methods"}`:                 "reserved",
		"{\"name\":\"a\"}\n{\"name\":\"a\"}": "declared twice",
	} {
		if _, err := ParseMethods("r", answer); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseMethods(%q) = %v, want %q", answer, err, want)
		}
	}
	exports, err := ParseMethods("r", `{"name":"plan"}`)
	if err != nil || len(exports) != 1 || exports[0].Signature.Params[0] != "string" || !exports[0].Signature.Variadic || exports[0].Signature.Results[0] != "string" {
		t.Fatalf("default signature: %+v, %v", exports, err)
	}
}

func TestMaterializeTextRejectsPaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../a", "/etc/x", "a/../../b"} {
		if _, err := materializeText("k", name, "x"); err == nil {
			t.Errorf("file name %q accepted", name)
		}
	}
	// A row may nest its artifact inside the fence root.
	file, err := materializeText("k-nested", "skill/SKILL.md", "---\nname: skill\n---\n")
	if err != nil || filepath.Base(filepath.Dir(file)) != "skill" || filepath.Base(filepath.Dir(filepath.Dir(file))) != "k-nested" {
		t.Fatalf("nested file name: %q, %v", file, err)
	}
}

func TestTextShadowAndOverlay(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "src", "main.rs"), []byte("fn main(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := ToolResolver
	var seen []string
	ToolResolver = func(string) ([]string, string, error) {
		return []string{"/bin/sh", "-c", `printf '%s\n' "$@"`, "argv0"}, "", nil
	}
	t.Cleanup(func() { ToolResolver = saved })
	text := Text{Type: "shadowed", FileName: "Cargo.toml", Tool: "fake", Shadow: []string{"src", "tests"}, Overlay: []string{"go.sum"},
		Dir: cwd, Verbs: []Verb{{Name: "build", Args: []string{"--manifest", "{file}", "--overlay", "{overlay}"}}}}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "shadowed", Alias: "s", Source: "[package]\n"}}, map[string]Analyzer{"shadowed": text})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Start(plans[0], text).Call(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	seen = strings.Split(result.Value.(string), "\n")
	dir := filepath.Join(textRoot(), plans[0].ID)
	// The shadow: src linked, the absent tests skipped, the caller's tree untouched.
	if link, err := os.Readlink(filepath.Join(dir, "src")); err != nil || link != filepath.Join(cwd, "src") {
		t.Fatalf("src shadow = %q, %v", link, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "tests")); err == nil {
		t.Fatal("absent entry was shadowed")
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 1 {
		t.Fatalf("caller's tree changed: %v", entries)
	}
	// The overlay: manifest and sibling mapped to the artifact dir, sibling created empty.
	overlay := filepath.Join(dir, "overlay.json")
	if seen[3] != overlay {
		t.Fatalf("argv = %v", seen)
	}
	var parsed struct{ Replace map[string]string }
	data, _ := os.ReadFile(overlay)
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Replace[filepath.Join(cwd, "Cargo.toml")] != filepath.Join(dir, "Cargo.toml") || parsed.Replace[filepath.Join(cwd, "go.sum")] != filepath.Join(dir, "go.sum") {
		t.Fatalf("overlay = %v", parsed.Replace)
	}
	if info, err := os.Stat(filepath.Join(dir, "go.sum")); err != nil || info.Size() != 0 {
		t.Fatalf("sibling not created empty: %v", err)
	}
}

// TestTextDiscoveredMethods covers the skill row's shape: a row with a
// fixed verb table (`run`) plus Discover, whose answer adds methods for
// targets a tasks.md-style file declares, each running through
// DiscoveredVerb's template. A discovered method's own call must agree
// with the declared verb's exec/error shape, and an empty Discover answer
// must add nothing (the untasked-skill case).
func TestTextDiscoveredMethods(t *testing.T) {
	saved := ToolResolver
	t.Cleanup(func() { ToolResolver = saved })
	ToolResolver = func(string) ([]string, string, error) {
		return []string{"/bin/sh", "-c", `
if [ "$1" = "list" ]; then
	printf '{"name":"send","effects":["net"]}\n\n'
else
	printf 'ran %s\n' "$2"
fi
`, "fake"}, "fake", nil
	}
	text := Text{Type: "tasked", FileName: "task.md", Tool: "fake-tool", Verbs: []Verb{
		{Name: "run", Args: []string{"run"}},
	},
		Discover:       &Verb{Args: []string{"list"}},
		DiscoveredVerb: &Verb{Args: []string{"target", "{target}"}},
	}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "tasked", Alias: "t", Source: "x\n"}}, map[string]Analyzer{"tasked": text})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	if len(plan.Exports) != 2 || plan.Exports[0].Name != "run" || plan.Exports[1].Name != "send" || strings.Join(plan.Exports[1].Effects, ",") != "net" {
		t.Fatalf("exports = %+v", plan.Exports)
	}
	module := Start(plan, text)
	result, err := module.Call(ctx, "send")
	if err != nil || result.Value != "ran send" {
		t.Fatalf("send = %+v, %v", result, err)
	}
	// Denial still reaches the processor, not a stub: an unknown discovered
	// name is the tool's own refusal, carried as the call's error.
	ToolResolver = func(string) ([]string, string, error) {
		return []string{"/bin/sh", "-c", `echo "no target" >&2; exit 1`}, "fake", nil
	}
	if _, err := module.Call(ctx, "send"); err == nil {
		t.Fatal("expected the processor's own refusal")
	}
}

// TestTextDiscoveredCallDeniesWidenedEffects: a target's effects grow
// between Discover (plan-build time) and the call (run time) — the recheck
// immediately before execution must deny it, because the only approval on
// record (the plan's Export) audited the narrower, stale declaration.
func TestTextDiscoveredCallDeniesWidenedEffects(t *testing.T) {
	saved := ToolResolver
	t.Cleanup(func() { ToolResolver = saved })
	list := func(effects string) func(string) ([]string, string, error) {
		return func(string) ([]string, string, error) {
			return []string{"/bin/sh", "-c", fmt.Sprintf(`
if [ "$1" = "list" ]; then
	printf '{"name":"send","effects":[%s]}\n'
else
	printf 'ran %%s\n' "$2"
fi
`, effects), "fake"}, "fake", nil
		}
	}
	ToolResolver = list(`"read"`)
	text := Text{Type: "widened", FileName: "task.md", Tool: "fake-tool", Verbs: []Verb{
		{Name: "run", Args: []string{"run"}},
	},
		Discover:       &Verb{Args: []string{"list"}},
		DiscoveredVerb: &Verb{Args: []string{"target", "{target}"}},
	}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "widened", Alias: "w", Source: "x\n"}}, map[string]Analyzer{"widened": text})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	if len(plan.Exports) != 2 || plan.Exports[1].Name != "send" || strings.Join(plan.Exports[1].Effects, ",") != "read" {
		t.Fatalf("exports = %+v", plan.Exports)
	}

	// The artifact widens (read -> read+net) after Discover ran.
	ToolResolver = list(`"read","net"`)
	module := Start(plan, text)
	if _, err := module.Call(ctx, "send"); err == nil {
		t.Fatal("expected denial of a target whose effects widened since discovery")
	}
}

func TestTextDiscoveredCallDeniesNewAgenticRequirement(t *testing.T) {
	saved := ToolResolver
	t.Cleanup(func() { ToolResolver = saved })
	list := func(agentic string) func(string) ([]string, string, error) {
		return func(string) ([]string, string, error) {
			return []string{"/bin/sh", "-c", fmt.Sprintf(`
if [ "$1" = "list" ]; then
	printf '{"name":"send","effects":["read"],"agentic":%s}\n'
else
	printf 'ran %%s\n' "$2"
fi
`, agentic), "fake"}, "fake", nil
		}
	}
	ToolResolver = list(`false`)
	text := Text{Type: "widened", FileName: "task.md", Tool: "fake-tool", Verbs: []Verb{
		{Name: "run", Args: []string{"run"}},
	},
		Discover:       &Verb{Args: []string{"list"}},
		DiscoveredVerb: &Verb{Args: []string{"target", "{target}"}},
	}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "widened", Alias: "w", Source: "x\n"}}, map[string]Analyzer{"widened": text})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	if len(plan.Exports) != 2 || plan.Exports[1].Name != "send" || strings.Join(plan.Exports[1].Effects, ",") != "read" {
		t.Fatalf("exports = %+v", plan.Exports)
	}

	// Effects stay identical while the agentic requirement appears.
	ToolResolver = list(`true`)
	module := Start(plan, text)
	if _, err := module.Call(ctx, "send"); err == nil {
		t.Fatal("expected denial of a newly agentic target")
	}
}

// TestTextDiscoveredCallRefusesUndeclaredMethod: a name DiscoveredVerb's
// template can spell, but that Discover never actually answered, must be
// refused — membership in the plan's Exports table gates the call, not
// mere syntactic shape.
func TestTextDiscoveredCallRefusesUndeclaredMethod(t *testing.T) {
	saved := ToolResolver
	t.Cleanup(func() { ToolResolver = saved })
	ToolResolver = func(string) ([]string, string, error) {
		return []string{"/bin/sh", "-c", `
if [ "$1" = "list" ]; then
	printf '{"name":"send","effects":["net"]}\n'
else
	printf 'ran %s\n' "$2"
fi
`, "fake"}, "fake", nil
	}
	text := Text{Type: "undeclared", FileName: "task.md", Tool: "fake-tool", Verbs: []Verb{
		{Name: "run", Args: []string{"run"}},
	},
		Discover:       &Verb{Args: []string{"list"}},
		DiscoveredVerb: &Verb{Args: []string{"target", "{target}"}},
	}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "undeclared", Alias: "u", Source: "x\n"}}, map[string]Analyzer{"undeclared": text})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], text)
	// "destroy" was added to the artifact only after Discover ran (or was
	// simply never there): it is not a member of plan.Exports, so it must
	// stay unreachable even though DiscoveredVerb could spell its call.
	if _, err := module.Call(ctx, "destroy"); err == nil {
		t.Fatal("expected refusal of a method name Discover never answered")
	}
}

// TestTextDiscoverEmptyKeepsDeclaredVerbsOnly is the skill-without-tasks.md
// case: Discover is set but answers nothing, so Analyze adds no methods —
// not a refusal.
func TestTextDiscoverEmptyKeepsDeclaredVerbsOnly(t *testing.T) {
	saved := ToolResolver
	t.Cleanup(func() { ToolResolver = saved })
	ToolResolver = func(string) ([]string, string, error) {
		return []string{"/bin/sh", "-c", `true`}, "fake", nil
	}
	text := Text{Type: "untasked", FileName: "task.md", Tool: "fake-tool", Verbs: []Verb{
		{Name: "run", Args: []string{"run"}},
	},
		Discover:       &Verb{Args: []string{"list"}},
		DiscoveredVerb: &Verb{Args: []string{"target", "{target}"}},
	}
	ctx := context.Background()
	plans, err := Prepare(ctx, []Block{{Language: "untasked", Alias: "u", Source: "x\n"}}, map[string]Analyzer{"untasked": text})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans[0].Exports) != 1 || plans[0].Exports[0].Name != "run" {
		t.Fatalf("exports = %+v", plans[0].Exports)
	}
}

func TestManifestFilesAndGoModule(t *testing.T) {
	RegisterLanguage(func() Language {
		row := TextRow("fakemod", nil, Text{Type: "fakemod", FileName: "go.mod", Tool: "go", Verbs: []Verb{{Name: "tidy"}}})
		row.ModuleFor = "go"
		return row
	}())
	t.Cleanup(func() {
		languagesMu.Lock()
		delete(languages, "fakemod")
		languagesMu.Unlock()
	})
	blocks := []Block{
		{Language: "fakemod", Alias: "mod", Source: "module example.com/m\n\ngo 1.27\n"},
		{Language: "go", Alias: "g", Source: "func F() int { return 1 }"},
	}
	files, err := ManifestFiles(blocks)
	if err != nil {
		t.Fatal(err)
	}
	file := files["go"]
	if filepath.Base(file) != "go.mod" {
		t.Fatalf("manifest for go = %q", file)
	}
	// The same key Prepare computes for the block: one dir, one manifest.
	plans, err := Prepare(context.Background(), blocks[:1], map[string]Analyzer{"fakemod": Text{Type: "fakemod", Verbs: []Verb{{Name: "tidy"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(file)) != plans[0].ID {
		t.Fatalf("manifest dir %s is not the plan id %s", filepath.Dir(file), plans[0].ID)
	}
	// Source origin is part of the same identity on both paths.
	blocks[0].Origin = filepath.Join(t.TempDir(), "module.bpp")
	files, err = ManifestFiles(blocks)
	if err != nil {
		t.Fatal(err)
	}
	plans, err = Prepare(context.Background(), blocks[:1], map[string]Analyzer{"fakemod": Text{Type: "fakemod", Verbs: []Verb{{Name: "tidy"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(filepath.Dir(files["go"])); got != plans[0].ID {
		t.Fatalf("manifest dir %s is not origin-aware plan id %s", got, plans[0].ID)
	}
	// A directory without go.mod is a module once a manifest fence provides one.
	cwd := t.TempDir()
	plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(cwd, "t.bsh"), Language: "go", ModuleFile: file})
	if err != nil {
		t.Fatalf("discover with a module file: %v", err)
	}
	if plan.Root != cwd || plan.ModuleFile != file || len(plan.Manifests) != 1 || plan.Manifests[0] != file {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(cwd, "t.bsh"), Language: "go"}); err == nil || !strings.Contains(err.Error(), "gomod fence") {
		t.Fatalf("without a module: %v", err)
	}
}

func TestParseMethodsAgentic(t *testing.T) {
	exports, err := ParseMethods("agent", `{"name":"run","agentic":true,"effect":"exec, net, spend"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !exports[0].Agentic || strings.Join(exports[0].Effects, ",") != "exec,net,spend" {
		t.Fatalf("lost governance: %+v", exports[0])
	}
	if _, err := ParseMethods("agent", `{"name":"run","agentic":"true"}`); err == nil {
		t.Fatal("accepted malformed agentic flag")
	}
	exports, err = ParseMethods("old", `{"name":"run"}`)
	if err != nil || exports[0].Agentic {
		t.Fatalf("legacy method changed: %+v %v", exports, err)
	}
}

func TestEmbeddedModuleClose(t *testing.T) {
	closed := 0
	runtime := Embedded{RuntimeName: "lifecycle", CallFunc: func(ctx context.Context, _ Plan, _ string, _ []any, _ map[string]any) (CallResult, error) {
		return CallResult{}, ctx.Err()
	}, CloseFunc: func(plan Plan) error {
		if plan.ID != "owned" {
			t.Fatalf("wrong owner: %+v", plan)
		}
		closed++
		return nil
	}}
	mod := Start(Plan{ID: "owned"}, runtime)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mod.Call(ctx, "run"); err == nil {
		t.Fatal("cancelled call succeeded")
	}
	if err := mod.Close(); err != nil || closed != 1 {
		t.Fatalf("close count=%d err=%v", closed, err)
	}
}
