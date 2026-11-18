package lower

import (
	"encoding/json"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// minimumGoSDK is the toolchain baseline the Bash++ lowering targets. It is
// reported, never assumed: an SDK older than this is named exactly as itself so
// a build is never silently credited with Go 1.27 capability it does not have.
const minimumGoSDK = "go1.27.0"

// goSDK is an installed Go SDK resolved to an absolute go command together with
// the GOROOT and toolchain version that command actually selects for a
// directory. Bin and Root stay coherent -- Bin is Root/bin/go whenever that file
// exists -- so module resolution, GOPATH resolution and internal-package
// visibility all speak about one and the same SDK.
//
// Resolution never derives a command from a bare runtime.GOROOT(): a binary
// built with -trimpath reports an empty GOROOT, and joining it would yield the
// relative path "bin/go", which execs whatever happens to sit under the current
// working directory (usually nothing, hence "fork/exec bin/go: no such file or
// directory"). Every candidate here is required to be absolute.
type goSDK struct {
	Bin     string // absolute path of the go command; never relative
	Root    string // absolute GOROOT reported by Bin
	Version string // toolchain version reported by Bin, e.g. "go1.27.0"
	GOMOD   string // GOMOD reported for the resolving directory
	GOPATH  string // GOPATH reported for the resolving directory
	Source  string // how the SDK was found, for diagnostics
}

// goToolchainSetting returns the GOTOOLCHAIN setting used for every go
// invocation. A deliberate GOTOOLCHAIN -- including one naming a specific
// toolchain -- is honoured verbatim. When the caller set nothing we default to
// "local", which resolves only what is already installed and so never downloads
// a toolchain or contacts the network on its own initiative.
func goToolchainSetting() string {
	if value, ok := os.LookupEnv("GOTOOLCHAIN"); ok && value != "" {
		return "GOTOOLCHAIN=" + value
	}
	return "GOTOOLCHAIN=local"
}

// env returns environ with GOROOT pinned to the resolved SDK, so a stale or
// absent GOROOT in the ambient environment cannot desynchronise the go command
// we chose from the standard library it resolves against.
func (s *goSDK) env(environ []string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, entry := range environ {
		if strings.HasPrefix(entry, "GOROOT=") || strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "GOROOT="+s.Root, goToolchainSetting())
}

// atLeast reports whether the resolved toolchain version is minimum or newer.
// An unparseable version (a devel build, say) is reported as not satisfying the
// baseline rather than being assumed to satisfy it.
func (s *goSDK) atLeast(minimum string) bool {
	return version.IsValid(s.Version) && version.Compare(s.Version, minimum) >= 0
}

// describe names the selected SDK precisely, including whether it actually meets
// the targeted baseline.
func (s *goSDK) describe() string {
	reported := s.Version
	if reported == "" {
		reported = "unknown version"
	}
	relation := "older than"
	if s.atLeast(minimumGoSDK) {
		relation = "meets"
	}
	return fmt.Sprintf("go SDK %s at %s (GOROOT=%s, found via %s, %s %s)",
		reported, s.Bin, s.Root, s.Source, relation, minimumGoSDK)
}

// goSDKCapability resolves the SDK for dir and reports the toolchain version it
// selects along with whether that version satisfies minimumGoSDK. The version is
// never inferred from the running process: a selected SDK older than the
// baseline is reported as itself.
func goSDKCapability(dir string) (string, bool, error) {
	sdk, err := resolveGoSDK(dir)
	if err != nil {
		return "", false, err
	}
	return sdk.Version, sdk.atLeast(minimumGoSDK), nil
}

func goCommandName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// isExecutableFile reports whether path names a regular file we may exec.
func isExecutableFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// goSDKCandidate is one place a Go SDK may live. root is empty for candidates
// discovered on PATH, where the go command itself is asked for its GOROOT.
type goSDKCandidate struct {
	source string
	root   string
	bin    string
}

// goSDKCandidates lists the SDKs to try, in order: an explicit GOROOT, the
// GOROOT baked into this binary, then a go command on PATH. Candidates that
// would produce a relative or missing command are dropped here rather than
// being exec'd.
func goSDKCandidates() ([]goSDKCandidate, []string) {
	var candidates []goSDKCandidate
	var rejected []string
	seen := make(map[string]bool)

	add := func(source, root, bin string) {
		if !filepath.IsAbs(bin) {
			rejected = append(rejected, fmt.Sprintf("%s: %q is not an absolute command", source, bin))
			return
		}
		if !isExecutableFile(bin) {
			rejected = append(rejected, fmt.Sprintf("%s: no executable go command at %s", source, bin))
			return
		}
		if seen[bin] {
			return
		}
		seen[bin] = true
		candidates = append(candidates, goSDKCandidate{source: source, root: root, bin: bin})
	}

	fromRoot := func(source, root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			rejected = append(rejected, source+": empty")
			return
		}
		if !filepath.IsAbs(root) {
			rejected = append(rejected, fmt.Sprintf("%s: %q is not an absolute GOROOT", source, root))
			return
		}
		add(source, root, filepath.Join(root, "bin", goCommandName()))
	}

	// BASHPP_GO is the embedder's explicit toolchain injection — bashy passes
	// its own provisioned go here when the host go is missing or older than
	// the baseline — and like the other BASHPP_* runtime overrides it names
	// the exact binary to use, so it is tried before everything else.
	if injected := strings.TrimSpace(os.Getenv("BASHPP_GO")); injected != "" {
		add("BASHPP_GO", "", injected)
		if len(candidates) != 0 {
			return candidates, rejected
		}
	}

	// The embedder's tool resolver is the island rule applied to the Go
	// front end: with one set, the toolchain comes from it — provisioned,
	// pinned — and the host's GOROOT/PATH are never consulted, so a program
	// lowers the same way on every host.
	if polyglot.ToolResolver != nil {
		if argv, _, err := polyglot.ToolResolver("go"); err != nil {
			rejected = append(rejected, "resolver: "+err.Error())
		} else if len(argv) != 1 {
			rejected = append(rejected, fmt.Sprintf("resolver: %q is not a single go binary", argv))
		} else {
			add("resolver", "", argv[0])
		}
		return candidates, rejected
	}

	fromRoot("GOROOT", os.Getenv("GOROOT"))
	// A -trimpath build reports an empty runtime GOROOT; fromRoot rejects it
	// instead of synthesising the relative command "bin/go".
	fromRoot("runtime.GOROOT()", runtime.GOROOT())

	// LookPath reports relative PATH entries as an error of its own since Go
	// 1.19; add rejects any that slip through, because executing a command
	// resolved against the current directory is exactly the failure mode here.
	if bin, err := exec.LookPath(goCommandName()); err != nil {
		rejected = append(rejected, "PATH: "+err.Error())
	} else {
		add("PATH", "", bin)
	}

	return candidates, rejected
}

// resolveGoSDK finds an installed Go SDK usable from dir. The go command is
// asked for its own environment, so GOTOOLCHAIN selection, GOMOD and GOPATH are
// all read from the toolchain that will actually run, not guessed.
func resolveGoSDK(dir string) (*goSDK, error) {
	candidates, reasons := goSDKCandidates()
	for _, candidate := range candidates {
		sdk, err := probeGoSDK(dir, candidate)
		if err != nil {
			reasons = append(reasons, candidate.source+": "+err.Error())
			continue
		}
		return sdk, nil
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no candidates")
	}
	return nil, fmt.Errorf("no usable Go SDK found (%s); set GOROOT to an installed SDK or put go on PATH",
		strings.Join(reasons, "; "))
}

func probeGoSDK(dir string, candidate goSDKCandidate) (*goSDK, error) {
	cmd := exec.Command(candidate.bin, "env", "-json", "GOROOT", "GOVERSION", "GOMOD", "GOPATH")
	cmd.Dir = dir

	environ := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOROOT=") || strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			continue
		}
		environ = append(environ, entry)
	}
	if candidate.root != "" {
		environ = append(environ, "GOROOT="+candidate.root)
	}
	cmd.Env = append(environ, goToolchainSetting())

	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s env failed: %w%s", candidate.bin, err, formatSDKStderr(stderr.String()))
	}

	var reported struct{ GOROOT, GOVERSION, GOMOD, GOPATH string }
	if err := json.Unmarshal(out, &reported); err != nil {
		return nil, fmt.Errorf("%s env produced unreadable output: %w", candidate.bin, err)
	}

	root := strings.TrimSpace(reported.GOROOT)
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%s reported unusable GOROOT %q", candidate.bin, reported.GOROOT)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s reported GOROOT %q which is not a directory", candidate.bin, root)
	}

	sdk := &goSDK{
		Bin:     candidate.bin,
		Root:    root,
		Version: strings.TrimSpace(reported.GOVERSION),
		GOMOD:   strings.TrimSpace(reported.GOMOD),
		GOPATH:  strings.TrimSpace(reported.GOPATH),
		Source:  candidate.source,
	}
	// GOTOOLCHAIN may have selected a different toolchain than the command we
	// probed. Prefer the selected toolchain's own command so Bin and Root always
	// describe one SDK.
	if selected := filepath.Join(root, "bin", goCommandName()); isExecutableFile(selected) {
		sdk.Bin = selected
	}
	return sdk, nil
}

func formatSDKStderr(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return ": " + text
}
