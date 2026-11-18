package polyglot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func graphAsset(id, path, source string) CSharpAsset {
	s := sha256.Sum256([]byte(source))
	return CSharpAsset{ID: id, Path: path, SHA256: hex.EncodeToString(s[:]), Data: []byte(source)}
}
func graphFixture() CSharpAssemblyGraph {
	return CSharpAssemblyGraph{Schema: "csharp-assembly-graph-v1", Root: "root", Sources: []CSharpAsset{
		graphAsset("base", "base.cs", "public class Base { protected internal static int Value = 2; }"),
		graphAsset("root", "root.cs", "public class Child : Base { public static int Read() => Value; }"),
	}, Nodes: []CSharpAssemblyNode{
		{ID: "base", Name: "Assembly1", Version: "0.0.0.0", Culture: "neutral", PublicKeyToken: "null", Output: "base/Assembly1.dll", Sources: []string{"base"}},
		{ID: "root", Name: "Assembly2", Version: "0.0.0.0", Culture: "neutral", PublicKeyToken: "null", Output: "root/Assembly2.dll", Sources: []string{"root"}, References: []CSharpAssemblyReference{{Node: "base", Aliases: []string{"global"}}}},
	}}
}
func TestCSharpGraphValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*CSharpAssemblyGraph)
	}{
		{"cycle", func(g *CSharpAssemblyGraph) {
			g.Nodes[0].References = []CSharpAssemblyReference{{Node: "root", Aliases: []string{"global"}}}
		}},
		{"traversal", func(g *CSharpAssemblyGraph) { g.Sources[0].Path = "../base.cs" }},
		{"drive", func(g *CSharpAssemblyGraph) { g.Sources[0].Path = "C:/base.cs" }},
		{"case collision", func(g *CSharpAssemblyGraph) { g.Sources[1].Path = "BASE.CS" }},
		{"missing reference", func(g *CSharpAssemblyGraph) { g.Nodes[1].References[0].Node = "missing" }},
		{"empty aliases", func(g *CSharpAssemblyGraph) { g.Nodes[1].References[0].Aliases = nil }},
		{"duplicate alias", func(g *CSharpAssemblyGraph) { g.Nodes[1].References[0].Aliases = []string{"x", "x"} }},
		{"digest", func(g *CSharpAssemblyGraph) { g.Sources[0].Data[0] = 'X' }},
		{"version", func(g *CSharpAssemblyGraph) { g.Nodes[0].Version = "65535.0.0.0" }},
		{"unreachable", func(g *CSharpAssemblyGraph) { g.Nodes[1].References = nil }},
		{"signing", func(g *CSharpAssemblyGraph) { g.Nodes[0].PublicKeyToken = "0011223344556677" }},
	}
	if err := graphFixture().validate(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			g := graphFixture()
			tt.change(&g)
			if g.validate() == nil {
				t.Fatal("invalid graph admitted")
			}
		})
	}
	for _, input := range []string{`{"schema":"a","schema":"b"}`, `{"unknown":1}`, `{} {}`, `{"nodes":[{"id":"a","id":"b"}]}`} {
		var g CSharpAssemblyGraph
		if strictCSharpJSON([]byte(input), &g) == nil {
			t.Fatalf("admitted %s", input)
		}
	}
}
func writeGraphFixture(t *testing.T, dir string) string {
	t.Helper()
	g := graphFixture()
	for i := range g.Sources {
		a := &g.Sources[i]
		if e := os.WriteFile(filepath.Join(dir, a.Path), a.Data, 0600); e != nil {
			t.Fatal(e)
		}
		a.Data = nil
	}
	b, _ := json.Marshal(g)
	if e := os.WriteFile(filepath.Join(dir, "graph.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	resolved, e := resolveCSharpAssemblies(dir, "graph.json")
	if e != nil {
		t.Fatal(e)
	}
	return resolved
}
func TestCSharpGraphSnapshot(t *testing.T) {
	d := t.TempDir()
	g := writeGraphFixture(t, d)
	other := writeGraphFixture(t, t.TempDir())
	if g != other {
		t.Fatal("relocation changed graph identity")
	}
	p := filepath.Join(d, "base.cs")
	info, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	b[len(b)-2] = 'X'
	os.WriteFile(p, b, 0600)
	os.Chtimes(p, info.ModTime(), info.ModTime())
	if _, e := resolveCSharpAssemblies(d, "graph.json"); e == nil {
		t.Fatal("same size/mtime mutation admitted")
	}
	var snapshot CSharpAssemblyGraph
	if e := strictCSharpJSON([]byte(g), &snapshot); e != nil {
		t.Fatal(e)
	}
	if e := snapshot.validate(); e != nil {
		t.Fatal(e)
	}
}
func TestCSharpGraphRuntime(t *testing.T) {
	c := cSharpRuntime(t)
	c.Environment = &EnvironmentPlan{CSharpAssemblies: writeGraphFixture(t, t.TempDir())}
	// Command remains the explicitly selected provider; the empty environment
	// plan contains only embedded assets in this direct API test.
	source := "public static int Observe() => Child.Read();"
	plans, e := Prepare(context.Background(), []Block{{Language: "csharp", Filename: "observer.bsh", Line: 1, Source: source}}, map[string]Analyzer{"csharp": c})
	if e != nil {
		t.Fatal(e)
	}
	if len(plans) != 1 || plans[0].CSharpAssemblies == "" {
		t.Fatal("graph missing from immutable plan")
	}
	m := Start(plans[0], c)
	defer m.Close()
	v, e := m.Call(context.Background(), "Observe")
	if e != nil {
		t.Fatal(e)
	}
	if v.Value != int64(2) {
		t.Fatalf("Observe=%#v", v)
	}
	u, e := c.graphUnit(plans[0].Source, plans[0].CSharpAssemblies)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := u.receipt(); !ok {
		t.Fatal("missing receipt")
	}
	// Release the loaded PE before simulating offline cache corruption. Windows
	// keeps loaded DLL files locked for the lifetime of the worker process.
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(u.dependencies[0], []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, ok := u.receipt(); ok {
		t.Fatal("corrupt dependency admitted")
	}
	if _, _, e = c.ensureGraph(context.Background(), plans[0].Source, plans[0].CSharpAssemblies); e != nil {
		t.Fatal(e)
	}
	g := graphFixture()
	g.Nodes[1].References[0].Aliases = []string{"Hidden"}
	b, _ := json.Marshal(g)
	c.Environment.CSharpAssemblies = string(b)
	_, e = c.Analyze(context.Background(), source)
	if e == nil || !strings.Contains(e.Error(), "CS0246") {
		t.Fatalf("direct alias isolation: %v", e)
	}
	var diagnostic *CSharpGraphError
	if !errors.As(e, &diagnostic) || diagnostic.Stage != "root" || len(diagnostic.Diagnostics) == 0 {
		t.Fatalf("missing structured compiler stage: %v", e)
	}
	d := diagnostic.Diagnostics[0]
	if d.ID != "CS0246" || d.Path != "root.cs" || d.Line != 1 || d.Column < 1 || d.Severity != "error" {
		t.Fatalf("invalid structured diagnostic: %#v", d)
	}
}

func TestCSharpGraphContentIdentity(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pwsh.exe")
	if e := os.WriteFile(exe, []byte("provider"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(dir, "ref"), 0700); e != nil {
		t.Fatal(e)
	}
	ref := filepath.Join(dir, "ref", "System.Runtime.dll")
	os.WriteFile(ref, []byte("framework"), 0600)
	g := graphFixture()
	b, _ := json.Marshal(g)
	c := CSharp{Command: exe, CacheDir: t.TempDir()}
	a, e := c.graphUnit("#line 10 \"/one/program.bsh\"\npublic static int Observe() => Child.Read();", string(b))
	if e != nil {
		t.Fatal(e)
	}
	moved, e := c.graphUnit("#line 10 \"/two/program.bsh\"\npublic static int Observe() => Child.Read();", string(b))
	if e != nil {
		t.Fatal(e)
	}
	if a.key != moved.key {
		t.Fatal("absolute observer path affected graph identity")
	}
	info, _ := os.Stat(ref)
	os.WriteFile(ref, []byte("FRAMEWORK"), 0600)
	os.Chtimes(ref, info.ModTime(), info.ModTime())
	changed, e := c.graphUnit("#line 10 \"/two/program.bsh\"\npublic static int Observe() => Child.Read();", string(b))
	if e != nil {
		t.Fatal(e)
	}
	if changed.key == a.key {
		t.Fatal("same size/mtime provider mutation missed")
	}
}

func TestCSharpGraphOverlay(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			want := writeGraphFixture(t, dir)
			exe := filepath.Join(dir, "pwsh.exe")
			os.WriteFile(exe, []byte("provider"), 0700)
			data := `{"language":"csharp","runtime":"pwsh","csharp_assemblies":"graph.json"}`
			if format == "yaml" {
				data = "language: csharp\nruntime: pwsh\ncsharp_assemblies: graph.json\n"
			}
			os.WriteFile(filepath.Join(dir, "bashpp."+format), []byte(data), 0600)
			p, e := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(dir, "program.bsh"), Language: "csharp", Environ: []string{"BASHPP_PWSH=" + exe}})
			if e != nil {
				t.Fatal(e)
			}
			if p.CSharpAssemblies != want {
				t.Fatal("overlay lost embedded assets")
			}
		})
	}
}

func TestCSharpGraphExternAlias(t *testing.T) {
	c := cSharpRuntime(t)
	g := graphFixture()
	g.Nodes[1].References[0].Aliases = []string{"Other"}
	g.Sources[1] = graphAsset("root", "root.cs", "extern alias Other; public class Child : Other::Base { public static int Read() => Value; }")
	b, _ := json.Marshal(g)
	c.Environment = &EnvironmentPlan{CSharpAssemblies: string(b)}
	plan := cSharpPlan(t, c, "public static int Observe() => Child.Read();")
	m := Start(plan, c)
	defer m.Close()
	v, e := m.Call(context.Background(), "Observe")
	if e != nil || v.Value != int64(2) {
		t.Fatalf("extern alias result %#v: %v", v, e)
	}
}

// A missing receipt at load time must not outlive the caller while another
// publisher holds the graph lock. Analyze first, then remove only our cache.
func TestCSharpGraphCanceledLoad(t *testing.T) {
	c := cSharpRuntime(t)
	c.Environment = &EnvironmentPlan{CSharpAssemblies: writeGraphFixture(t, t.TempDir())}
	plan := cSharpPlan(t, c, "public static int Observe() => Child.Read();")
	u, err := c.graphUnit(plan.Source, c.graphPayload(plan))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(u.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(u.dir+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(u.dir + ".lock")
	m := Start(plan, c)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = m.Call(ctx, "Observe")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("load cancellation: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("load exceeded cancellation bound")
	}
	if m.alive() {
		t.Fatal("canceled load left worker alive")
	}
}

func TestCSharpGraphLoadContext(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pwsh.exe")
	if err := os.WriteFile(exe, []byte("provider"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "ref"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ref", "System.Runtime.dll"), []byte("framework"), 0600); err != nil {
		t.Fatal(err)
	}
	c := CSharp{Command: exe, CacheDir: t.TempDir()}
	plan := Plan{Source: "public static int Observe() => Child.Read();", CSharpAssemblies: writeGraphFixture(t, t.TempDir())}
	u, err := c.graphUnit(plan.Source, plan.CSharpAssemblies)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(u.dir+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	load := c.loadRequestContext(ctx, plan)
	if load["error"] != context.DeadlineExceeded.Error() {
		t.Fatalf("load=%v", load)
	}
	if time.Since(start) > time.Second {
		t.Fatal("load ignored deadline")
	}
}

func TestCSharpGraphManifestRelativePaths(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	if err := os.Mkdir(assets, 0700); err != nil {
		t.Fatal(err)
	}
	want := writeGraphFixture(t, assets)
	got, err := resolveCSharpAssemblies(dir, "assets/graph.json")
	if err != nil || got != want {
		t.Fatalf("nested manifest: %v", err)
	}
	// A native fixture packaging error must not enable an implicit fallback
	// to overlay-relative assets, even when those files exist.
	data, err := os.ReadFile(filepath.Join(assets, "graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	var graph CSharpAssemblyGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatal(err)
	}
	for i := range graph.Sources {
		graph.Sources[i].Path = "assets/" + graph.Sources[i].Path
	}
	data, err = json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(assets, "graph.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveCSharpAssemblies(dir, "assets/graph.json"); err == nil {
		t.Fatal("admitted overlay-relative paths in nested manifest")
	}
	// Relocating the exact manifest bytes restores the declared layout.
	if err = os.Rename(filepath.Join(assets, "graph.json"), filepath.Join(dir, "graph.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveCSharpAssemblies(dir, "graph.json"); err != nil {
		t.Fatal(err)
	}
}
