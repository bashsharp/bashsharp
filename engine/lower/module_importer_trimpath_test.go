//go:build full

package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const (
	trimpathChildVar = "LOWER_TRIMPATH_CHILD"
	trimpathDirVar   = "LOWER_TRIMPATH_DIR"
)

// trimpathFixture writes a module whose packages exercise every resolution path
// the importer takes: an ordinary package, a same-module internal package, and a
// Bash++ source that imports one of them.
func trimpathFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":                  "module example.test/app\n\ngo 1.25\n",
		"dep/dep.go":              "package dep\n\nimport \"fmt\"\n\nfunc Print() { fmt.Println(\"dep\") }\n",
		"internal/value/value.go": "package value\n\nconst N = 7\n",
		"main.bpp":                trimpathSource,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const trimpathSource = "import \"example.test/app/dep\"\ndep.Print()\n"

// TestModuleImporterTrimpathResolvesInstalledSDK builds this package's tests with
// -trimpath and runs them with GOROOT unset, reproducing the product artifact
// that Makefile-built binaries are. In that process runtime.GOROOT() is empty, so
// joining it yields the relative command "bin/go" that every import used to fail
// on with "fork/exec bin/go: no such file or directory". The child must instead
// discover the go command that the parent placed on PATH.
func TestModuleImporterTrimpathResolvesInstalledSDK(t *testing.T) {
	if os.Getenv(trimpathChildVar) == "1" {
		t.Skip("running as the -trimpath child")
	}
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := resolveGoSDK(packageDir)
	if err != nil {
		t.Skipf("no installed Go SDK to build the -trimpath child: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "lower.trimpath.test")
	// The child test lives in this full-tier file, so the child needs the tag.
	build := exec.Command(sdk.Bin, "test", "-c", "-trimpath", "-tags", "full", "-o", binary, ".")
	build.Dir = packageDir
	build.Env = sdk.env(os.Environ())
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building -trimpath child: %v\n%s", err, output)
	}

	root := trimpathFixture(t)

	// The child sees no GOROOT at all: the only way to a go command is the
	// toolchain directory on PATH. The path is taken from the resolved SDK, never
	// from a hardcoded host location.
	var environ []string
	for _, entry := range os.Environ() {
		switch {
		case strings.HasPrefix(entry, "GOROOT="),
			strings.HasPrefix(entry, "PATH="),
			strings.HasPrefix(entry, "GOTOOLCHAIN="),
			strings.HasPrefix(entry, trimpathChildVar+"="),
			strings.HasPrefix(entry, trimpathDirVar+"="):
		default:
			environ = append(environ, entry)
		}
	}
	pathEntries := []string{filepath.Dir(sdk.Bin), "/usr/bin", "/bin"}
	environ = append(environ,
		"PATH="+strings.Join(pathEntries, string(os.PathListSeparator)),
		goToolchainSetting(),
		trimpathChildVar+"=1",
		trimpathDirVar+"="+root,
		"GOWORK=off", "GO111MODULE=on", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=",
	)

	child := exec.Command(binary, "-test.run", "^TestModuleImporterTrimpathChild$", "-test.v")
	child.Dir = root
	child.Env = environ
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("-trimpath child failed: %v\n%s", err, output)
	}
	text := string(output)
	if strings.Contains(text, "bin/go: no such file or directory") {
		t.Fatalf("child still execs a relative go command:\n%s", text)
	}
	for _, marker := range []string{
		"trimpath-child: empty runtime GOROOT confirmed",
		"trimpath-child: module import ok",
		"trimpath-child: stdlib import ok",
		"trimpath-child: same-module internal import ok",
		"trimpath-child: foreign stdlib internal import rejected",
		"trimpath-child: Compile ok",
		"trimpath-child: capability reported",
		"trimpath-child: exhausted ladder reported",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("child never reported %q:\n%s", marker, text)
		}
	}
}

// TestModuleImporterTrimpathChild is the -trimpath half of
// TestModuleImporterTrimpathResolvesInstalledSDK. It is inert unless launched by
// that parent.
func TestModuleImporterTrimpathChild(t *testing.T) {
	if os.Getenv(trimpathChildVar) != "1" {
		t.Skip("child of TestModuleImporterTrimpathResolvesInstalledSDK")
	}
	if got := os.Getenv("GOROOT"); got != "" {
		t.Fatalf("child expects no GOROOT in the environment, got %q", got)
	}
	if got := runtime.GOROOT(); got != "" {
		t.Fatalf("child binary is not a -trimpath build: runtime.GOROOT() = %q", got)
	}
	if joined := filepath.Join(runtime.GOROOT(), "bin", "go"); filepath.IsAbs(joined) {
		t.Fatalf("expected the regressed join to be relative, got %q", joined)
	}
	t.Log("trimpath-child: empty runtime GOROOT confirmed")

	dir := os.Getenv(trimpathDirVar)
	if dir == "" {
		t.Fatal("child requires " + trimpathDirVar)
	}

	sdk, err := resolveGoSDK(dir)
	if err != nil {
		t.Fatalf("resolveGoSDK with GOROOT unset: %v", err)
	}
	if !filepath.IsAbs(sdk.Bin) || !isExecutableFile(sdk.Bin) {
		t.Fatalf("resolved go command is not an absolute executable: %q", sdk.Bin)
	}
	if !filepath.IsAbs(sdk.Root) {
		t.Fatalf("resolved GOROOT is not absolute: %q", sdk.Root)
	}
	if sdk.Source != "PATH" {
		t.Fatalf("with no GOROOT anywhere the SDK must come from PATH, got %s", sdk.Source)
	}

	imp := newModuleImporter(dir)
	pkg, err := imp.Import("example.test/app/dep")
	if err != nil {
		t.Fatalf("module import under -trimpath: %v", err)
	}
	if pkg.Name() != "dep" {
		t.Fatalf("module import resolved to %q", pkg.Name())
	}
	t.Log("trimpath-child: module import ok")

	if _, err := imp.Import("fmt"); err != nil {
		t.Fatalf("stdlib import under -trimpath: %v", err)
	}
	t.Log("trimpath-child: stdlib import ok")

	// Caller identity comes from `go list -m` run through the resolved SDK. With
	// no usable go command that lookup silently degrades and same-module internal
	// imports are rejected, so this is a direct regression on SDK resolution.
	if _, err := imp.Import("example.test/app/internal/value"); err != nil {
		t.Fatalf("same-module internal import under -trimpath: %v", err)
	}
	t.Log("trimpath-child: same-module internal import ok")

	// Visibility of GOROOT-internal packages is decided against the resolved
	// GOROOT, not an empty runtime one.
	if _, err := imp.Import("internal/abi"); err == nil {
		t.Fatal("stdlib internal package accepted from a non-GOROOT caller")
	}
	t.Log("trimpath-child: foreign stdlib internal import rejected")

	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(trimpathSource), "main.bpp")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Compile(file, Options{Dir: dir})
	if err != nil {
		t.Fatalf("Compile under -trimpath: %v", err)
	}
	if len(result.Source) == 0 {
		t.Fatal("Compile produced no source")
	}
	t.Log("trimpath-child: Compile ok")

	version, satisfies, err := goSDKCapability(dir)
	if err != nil {
		t.Fatalf("goSDKCapability: %v", err)
	}
	if version == "" {
		t.Fatal("capability reported an empty toolchain version")
	}
	if satisfies != sdk.atLeast(minimumGoSDK) {
		t.Fatalf("capability %v disagrees with resolved version %q", satisfies, version)
	}
	t.Logf("trimpath-child: capability reported %s satisfies %s: %v", version, minimumGoSDK, satisfies)

	// With an empty runtime GOROOT and nothing on PATH the ladder is genuinely
	// exhausted. That must be reported, not turned into an exec of "bin/go".
	t.Run("exhausted-ladder-is-reported", func(t *testing.T) {
		t.Setenv("PATH", "")
		if _, err := resolveGoSDK(dir); err == nil {
			t.Fatal("resolution succeeded with no SDK anywhere")
		} else if !strings.Contains(err.Error(), "no usable Go SDK found") {
			t.Fatalf("unexpected report: %v", err)
		}
		_, err := newModuleImporter(dir).Import("fmt")
		if err == nil {
			t.Fatal("import succeeded with no SDK anywhere")
		}
		message := err.Error()
		if !strings.Contains(message, "no usable Go SDK found") {
			t.Fatalf("import did not report the missing SDK: %v", err)
		}
		if strings.Contains(message, "fork/exec") || strings.Contains(message, "bin/go:") {
			t.Fatalf("import still exec'd a command: %v", err)
		}
	})
	t.Log("trimpath-child: exhausted ladder reported")
}

// TestModuleImporterGoSDKResolutionOrder covers the resolution ladder in the
// running process: an explicit GOROOT wins when it is valid, an invalid one is
// stepped over rather than exec'd, and exhausting every candidate is reported
// instead of degenerating into a relative command.
func TestModuleImporterGoSDKResolutionOrder(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := resolveGoSDK(dir)
	if err != nil {
		t.Skipf("no installed Go SDK: %v", err)
	}

	t.Run("explicit-goroot", func(t *testing.T) {
		t.Setenv("GOROOT", baseline.Root)
		sdk, err := resolveGoSDK(dir)
		if err != nil {
			t.Fatalf("valid explicit GOROOT rejected: %v", err)
		}
		if sdk.Source != "GOROOT" {
			t.Fatalf("explicit GOROOT not preferred, resolved via %s", sdk.Source)
		}
		if sdk.Root != baseline.Root {
			t.Fatalf("resolved GOROOT %q, want %q", sdk.Root, baseline.Root)
		}
		if !isExecutableFile(filepath.Join(sdk.Root, "bin", goCommandName())) {
			t.Fatalf("resolved GOROOT %q holds no go command", sdk.Root)
		}
	})

	// runtime.GOROOT() is baked into a non -trimpath test binary and no longer
	// tracks the environment, so an ordinary run can never exhaust the ladder.
	// Candidate construction is what decides which commands are exec'able at all,
	// and it is asserted directly here; the exhausted-ladder and PATH-fallback
	// cases are covered by TestModuleImporterTrimpathChild, where runtime.GOROOT()
	// really is empty.
	t.Run("unusable-goroot-is-skipped-not-exec'd", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-an-sdk")
		for _, unusable := range []string{"relative/sdk", "", missing} {
			t.Setenv("GOROOT", unusable)
			candidates, rejected := goSDKCandidates()
			for _, candidate := range candidates {
				if candidate.source == "GOROOT" {
					t.Fatalf("GOROOT=%q became candidate command %q", unusable, candidate.bin)
				}
				if !filepath.IsAbs(candidate.bin) || !isExecutableFile(candidate.bin) {
					t.Fatalf("candidate %s is not an absolute executable: %q", candidate.source, candidate.bin)
				}
			}
			if len(rejected) == 0 {
				t.Fatalf("GOROOT=%q was skipped without a reason", unusable)
			}
			joined := strings.Join(rejected, "; ")
			if !strings.Contains(joined, "GOROOT:") {
				t.Fatalf("GOROOT=%q rejection not reported: %s", unusable, joined)
			}
		}
	})

	t.Run("resolution-is-coherent", func(t *testing.T) {
		if !filepath.IsAbs(baseline.Bin) || !isExecutableFile(baseline.Bin) {
			t.Fatalf("resolved command %q is not an absolute executable", baseline.Bin)
		}
		if info, err := os.Stat(baseline.Root); err != nil || !info.IsDir() {
			t.Fatalf("resolved GOROOT %q is not a directory: %v", baseline.Root, err)
		}
		// GOMOD and GOPATH come from the same go command that will run the
		// imports, which is what keeps module, GOPATH and internal-visibility
		// decisions talking about one SDK.
		command := exec.Command(baseline.Bin, "env", "GOMOD", "GOPATH")
		command.Dir = dir
		command.Env = baseline.env(os.Environ())
		output, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) != 2 || lines[0] != baseline.GOMOD || lines[1] != baseline.GOPATH {
			t.Fatalf("SDK reported GOMOD=%q GOPATH=%q, go env says %q", baseline.GOMOD, baseline.GOPATH, output)
		}
	})
}

// TestModuleImporterGoSDKToolchainRespected pins the GOTOOLCHAIN policy: a
// deliberate setting is passed through untouched, an absent one defaults to
// "local" so nothing is ever downloaded, and no other proxy, sum-database or
// flag configuration is injected behind the caller's back.
func TestModuleImporterGoSDKToolchainRespected(t *testing.T) {
	t.Run("deliberate-setting-passed-through", func(t *testing.T) {
		for _, value := range []string{"local", "go1.27.1", "auto"} {
			t.Setenv("GOTOOLCHAIN", value)
			if got := goToolchainSetting(); got != "GOTOOLCHAIN="+value {
				t.Fatalf("GOTOOLCHAIN=%s became %q", value, got)
			}
		}
	})

	t.Run("absent-setting-defaults-to-local", func(t *testing.T) {
		t.Setenv("GOTOOLCHAIN", "")
		if got := goToolchainSetting(); got != "GOTOOLCHAIN=local" {
			t.Fatalf("unset GOTOOLCHAIN became %q, want the non-downloading default", got)
		}
	})

	t.Run("no-hidden-configuration", func(t *testing.T) {
		t.Setenv("GOTOOLCHAIN", "local")
		sdk := &goSDK{Bin: "/sdk/bin/go", Root: "/sdk"}
		base := []string{"HOME=/home/user", "GOPROXY=off", "GOROOT=/stale", "GOTOOLCHAIN=auto"}
		got := sdk.env(base)
		want := []string{"HOME=/home/user", "GOPROXY=off", "GOROOT=/sdk", "GOTOOLCHAIN=local"}
		if len(got) != len(want) {
			t.Fatalf("env %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("env %v, want %v", got, want)
			}
		}
	})
}

// TestModuleImporterGoSDKCapabilityNotAssumed checks that the targeted Go 1.27
// capability is reported from the SDK that will actually run, and that an older
// selected SDK is named as itself rather than credited with the baseline.
func TestModuleImporterGoSDKCapabilityNotAssumed(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := resolveGoSDK(dir)
	if err != nil {
		t.Skipf("no installed Go SDK: %v", err)
	}

	command := exec.Command(sdk.Bin, "version")
	command.Dir = dir
	command.Env = sdk.env(os.Environ())
	output, err := command.Output()
	if err != nil {
		t.Fatalf("%s version: %v", sdk.Bin, err)
	}
	if !strings.Contains(string(output), sdk.Version) {
		t.Fatalf("reported version %q absent from %q", sdk.Version, strings.TrimSpace(string(output)))
	}

	version, satisfies, err := goSDKCapability(dir)
	if err != nil {
		t.Fatal(err)
	}
	if version != sdk.Version {
		t.Fatalf("capability version %q, want %q", version, sdk.Version)
	}
	if satisfies != sdk.atLeast(minimumGoSDK) {
		t.Fatalf("capability %v disagrees with version %q", satisfies, version)
	}
	if !strings.Contains(sdk.describe(), sdk.Version) || !strings.Contains(sdk.describe(), sdk.Bin) {
		t.Fatalf("description omits the selected SDK: %s", sdk.describe())
	}

	older := &goSDK{Bin: sdk.Bin, Root: sdk.Root, Version: "go1.26.0", Source: "GOROOT"}
	if older.atLeast(minimumGoSDK) {
		t.Fatal("go1.26.0 credited with the go1.27.1 baseline")
	}
	if !strings.Contains(older.describe(), "older than "+minimumGoSDK) {
		t.Fatalf("older SDK not reported precisely: %s", older.describe())
	}
	if unknown := (&goSDK{Version: "devel +abcdef"}); unknown.atLeast(minimumGoSDK) {
		t.Fatal("unparseable version credited with the baseline")
	}
}
