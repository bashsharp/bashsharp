//go:build full

package lower

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func TestPreparedForeignRejectsCancellationAndAssetDrift(t *testing.T) {
	dir := t.TempDir()
	provider := filepath.Join(dir, "provider")
	asset := filepath.Join(dir, "unit.cppm")
	for path, content := range map[string]string{provider: "provider one", asset: "export module unit;"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	block := polyglot.Block{Language: "cpp", Source: "int value();"}
	blocks := []polyglot.Block{block}
	environ := []string{"PATH=/bin"}
	envs := map[string]*polyglot.EnvironmentPlan{"cpp": {
		Executable:    provider,
		NativeModules: []polyglot.NativeModuleUnit{{Name: "unit", Source: asset}},
	}}
	files, err := preparedFiles(envs)
	if err != nil {
		t.Fatal(err)
	}
	revision := polyglot.LanguageRevision()
	prepared := &preparedForeign{
		blocks: append([]polyglot.Block(nil), blocks...), source: "/src/main.bpp", dir: "/src",
		environ: append([]string(nil), environ...), foreignEnvs: cloneForeignEnvs(envs),
		revision: revision, plans: []polyglot.Plan{{Language: "cpp"}}, files: files,
	}
	check := func(ctx context.Context) bool {
		return prepared.reusable(ctx, blocks, "/src/main.bpp", "/src", environ, envs, revision)
	}
	if !check(context.Background()) {
		t.Fatal("unchanged preparation rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if check(ctx) {
		t.Fatal("canceled compilation reused preparation")
	}
	envs["cpp"].NativeModules[0].Source = filepath.Join(dir, "changed.cppm")
	if check(context.Background()) {
		t.Fatal("changed C++ source asset reused preparation")
	}
	envs["cpp"].NativeModules[0].Source = asset
	envs["cpp"].CSharpAssemblies = `{"source":"changed"}`
	if check(context.Background()) {
		t.Fatal("changed C# assembly graph reused preparation")
	}
	envs["cpp"].CSharpAssemblies = ""
	if err := os.WriteFile(asset, []byte("export module changed;"), 0600); err != nil {
		t.Fatal(err)
	}
	if check(context.Background()) {
		t.Fatal("same-path C++ module bytes reused preparation")
	}
	if err := os.WriteFile(asset, []byte("export module unit;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider, []byte("provider two"), 0600); err != nil {
		t.Fatal(err)
	}
	if check(context.Background()) {
		t.Fatal("same-path provider bytes reused preparation")
	}
	if err := os.WriteFile(provider, []byte("provider one"), 0600); err != nil {
		t.Fatal(err)
	}
	blocks[0].Source = "invalid source"
	if check(context.Background()) {
		t.Fatal("changed fence source reused preparation")
	}
}

func TestNativePreparationEligibility(t *testing.T) {
	for _, language := range []string{"csharp", "rust", "go", "python", "lowerprepareprobe"} {
		t.Run(language, func(t *testing.T) {
			if nativePreparationEligible([]polyglot.Block{{Language: language}}) {
				t.Fatal("other-language preparation eligible")
			}
			if nativePreparationEligible([]polyglot.Block{{Language: "cpp"}, {Language: language}}) {
				t.Fatal("mixed preparation eligible")
			}
		})
	}
	if nativePreparationEligible(nil) {
		t.Fatal("empty preparation eligible")
	}
	for _, language := range []string{"c", "cpp"} {
		if !nativePreparationEligible([]polyglot.Block{{Language: language}}) {
			t.Fatal("native preparation not eligible")
		}
	}
}
