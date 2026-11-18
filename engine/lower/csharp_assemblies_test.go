//go:build full

package lower_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestCSharpAssembliesBothModesWithoutSourceAssets(t *testing.T) {
	requirePowerShell(t)
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.Mkdir(assets, 0700); err != nil {
		t.Fatal(err)
	}
	g := polyglot.CSharpAssemblyGraph{Schema: "csharp-assembly-graph-v1", Root: "root"}
	for _, a := range []struct{ id, body string }{{"base", "public class Base { protected internal static int Value = 2; }"}, {"root", "public class Child : Base { public static int Read() => Value; }"}} {
		hash := sha256.Sum256([]byte(a.body))
		g.Sources = append(g.Sources, polyglot.CSharpAsset{ID: a.id, Path: a.id + ".cs", SHA256: hex.EncodeToString(hash[:])})
		if err := os.WriteFile(filepath.Join(assets, a.id+".cs"), []byte(a.body), 0600); err != nil {
			t.Fatal(err)
		}
		n := polyglot.CSharpAssemblyNode{ID: a.id, Name: "Graph_" + a.id, Version: "0.0.0.0", Culture: "neutral", PublicKeyToken: "null", Output: a.id + ".dll", Sources: []string{a.id}}
		if a.id == "root" {
			n.References = []polyglot.CSharpAssemblyReference{{Node: "base", Aliases: []string{"global"}}}
		}
		g.Nodes = append(g.Nodes, n)
	}
	data, _ := json.Marshal(g)
	if err := os.WriteFile(filepath.Join(assets, "graph.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	config := `{"language":"csharp","runtime":"pwsh","csharp_assemblies":"assets/graph.json"}`
	if err := os.WriteFile(filepath.Join(root, "bashpp.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	file := parse(t, "~~~csharp\npublic static int Observe() => Child.Read();\n~~~\nx := Observe()\necho $x\n", filepath.Join(root, "program.bsh"))
	result, err := lower.Compile(file, lower.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(root), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Run(context.Background(), file); err != nil {
		t.Fatalf("interpreted: %v %s", err, &stderr)
	}
	if out.String() != "2\n" || stderr.Len() != 0 {
		t.Fatalf("interpreted output %q %q", &out, &stderr)
	}
	if err = os.RemoveAll(assets); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, "bashpp.json")); err != nil {
		t.Fatal(err)
	}
	stdout, errout, status := runArtifact(t, result)
	if status != 0 || stdout != "2\n" || errout != "" {
		t.Fatalf("compiled without source assets: %d %q %q", status, stdout, errout)
	}
}
