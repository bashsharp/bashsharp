package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	sharedBshOnce sync.Once
	sharedBshDir  string
	sharedBshBin  string
	sharedBshErr  error

	sharedGoShimOnce sync.Once
	sharedGoShimBin  string
	sharedGoShimErr  error

	sharedRefProductGoOnce sync.Once
	sharedRefProductGoBin  string
	sharedRefProductGoErr  error
)

func buildTestBsh(t *testing.T) string {
	t.Helper()
	sharedBshOnce.Do(func() {
		sharedBshDir, sharedBshErr = os.MkdirTemp("", "bsh-bootstrap-*")
		if sharedBshErr != nil {
			return
		}
		name := "bsh"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		sharedBshBin = filepath.Join(sharedBshDir, name)
		cmd := exec.Command("go", "build", "-o", sharedBshBin, ".")
		cmd.Env = standaloneEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			sharedBshErr = &buildErr{msg: string(out), err: err}
		}
	})
	if sharedBshErr != nil {
		t.Fatalf("bootstrap bsh build: %v", sharedBshErr)
	}
	return sharedBshBin
}

type buildErr struct {
	msg string
	err error
}

func (e *buildErr) Error() string { return e.err.Error() + "\n" + e.msg }

const recordingGoShimSrc = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		realGo := os.Getenv("FAKE_GO_REAL_BIN")
		if realGo == "" {
			fmt.Fprintln(os.Stderr, "FAKE_GO_REAL_BIN unset")
			os.Exit(2)
		}
		cmd := exec.Command(realGo, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			os.Exit(1)
		}
		return
	case "env":
		vals := map[string]string{
			"GOROOT":     os.Getenv("FAKE_GOROOT"),
			"GOVERSION":  os.Getenv("FAKE_GOVERSION"),
			"GOOS":       firstNonEmpty(os.Getenv("GOOS"), os.Getenv("FAKE_GOOS"), runtime.GOOS),
			"GOARCH":     firstNonEmpty(os.Getenv("GOARCH"), runtime.GOARCH),
			"GOMODCACHE": os.Getenv("FAKE_GOMODCACHE"),
			"GOHOSTOS":   runtime.GOOS,
			"GOHOSTARCH": runtime.GOARCH,
		}
		if exe, err := os.Executable(); err == nil {
			// When invoked from a relocated SDK (<buildRoot>/bin/go), GOROOT in env wins.
			if gr := os.Getenv("GOROOT"); gr != "" {
				vals["GOROOT"] = gr
			} else if vals["GOROOT"] == "" {
				vals["GOROOT"] = filepath.Dir(filepath.Dir(exe))
			}
		}
		if len(os.Args) >= 3 && os.Args[2] == "-json" {
			out := map[string]string{}
			for _, k := range os.Args[3:] {
				out[k] = vals[k]
			}
			enc, _ := json.Marshal(out)
			os.Stdout.Write(enc)
			return
		}
		for _, k := range os.Args[2:] {
			fmt.Println(vals[k])
		}
		return
	case "build":
		capDir := os.Getenv("FAKE_CAPTURE_DIR")
		if capDir == "" {
			os.Exit(0)
		}
		argsJSON, _ := json.Marshal(os.Args[1:])
		if err := os.WriteFile(filepath.Join(capDir, "args.json"), argsJSON, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		envInfo := map[string]string{
			"GOROOT":      os.Getenv("GOROOT"),
			"GOTOOLCHAIN": os.Getenv("GOTOOLCHAIN"),
			"GOOS":        os.Getenv("GOOS"),
		}
		envBytes, _ := json.Marshal(envInfo)
		_ = os.WriteFile(filepath.Join(capDir, "env.json"), envBytes, 0600)
		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			var overlayPath string
			if strings.HasPrefix(arg, "-overlay=") {
				overlayPath = strings.TrimPrefix(arg, "-overlay=")
			} else if arg == "-overlay" && i+1 < len(os.Args) {
				overlayPath = os.Args[i+1]
			}
			if overlayPath != "" {
				ovBytes, err := os.ReadFile(overlayPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "read overlay %s: %v\n", overlayPath, err)
					os.Exit(1)
				}
				if err := os.WriteFile(filepath.Join(capDir, "overlay.json"), ovBytes, 0600); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				var parsed struct {
					Replace map[string]string
				}
				if err := json.Unmarshal(ovBytes, &parsed); err != nil {
					fmt.Fprintf(os.Stderr, "parse overlay: %v\n", err)
					os.Exit(1)
				}
				for _, repl := range parsed.Replace {
					sigBytes, err := os.ReadFile(repl)
					if err != nil {
						fmt.Fprintf(os.Stderr, "read replacement %s: %v\n", repl, err)
						os.Exit(1)
					}
					if err := os.WriteFile(filepath.Join(capDir, "signal_unix.go"), sigBytes, 0600); err != nil {
						fmt.Fprintln(os.Stderr, err)
						os.Exit(1)
					}
				}
			}
		}
		return
	default:
		os.Exit(0)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
`

func buildRecordingGoShim(t *testing.T) string {
	t.Helper()
	sharedGoShimOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bsh-goshim-*")
		if err != nil {
			sharedGoShimErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(recordingGoShimSrc), 0600); err != nil {
			sharedGoShimErr = err
			return
		}
		name := "go"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		sharedGoShimBin = filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", sharedGoShimBin, src)
		cmd.Env = standaloneEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			sharedGoShimErr = &buildErr{msg: string(out), err: err}
		}
	})
	if sharedGoShimErr != nil {
		t.Fatalf("build recording go shim: %v", sharedGoShimErr)
	}
	return sharedGoShimBin
}

func buildRefGoProductGo(t *testing.T, realGo string, bashModDir string) string {
	t.Helper()
	sharedRefProductGoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bsh-ref-productgo-*")
		if err != nil {
			sharedRefProductGoErr = err
			return
		}
		name := "productgo"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		sharedRefProductGoBin = filepath.Join(dir, name)
		mainGo := filepath.Join(bashModDir, "tools", "productgo", "main.go")
		cmd := exec.Command(realGo, "build", "-o", sharedRefProductGoBin, mainGo)
		cmd.Env = standaloneEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			sharedRefProductGoErr = &buildErr{msg: string(out), err: err}
		}
	})
	if sharedRefProductGoErr != nil {
		t.Fatalf("build reference Go productgo: %v", sharedRefProductGoErr)
	}
	return sharedRefProductGoBin
}

func requireToolchainGo1274(t *testing.T) (realGo string, realGoroot string, signalUnixBytes []byte, bashModDir string) {
	t.Helper()
	var err error
	realGo, err = exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate go: %v", err)
	}
	verOut, err := exec.Command(realGo, "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}
	if got := strings.TrimSpace(string(verOut)); got != "go1.27.4" {
		t.Fatalf("productgo test requires go1.27.4, got %q", got)
	}
	rootOut, err := exec.Command(realGo, "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	realGoroot = strings.TrimSpace(string(rootOut))
	signalUnixBytes, err = os.ReadFile(filepath.Join(realGoroot, "src", "runtime", "signal_unix.go"))
	if err != nil {
		t.Fatalf("read signal_unix.go: %v", err)
	}
	modCmd := exec.Command(realGo, "list", "-m", "-f", "{{.Dir}}", "github.com/qiangli/bash")
	modCmd.Env = standaloneEnv()
	modOut, err := modCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("locate github.com/qiangli/bash module: %v\n%s", err, modOut)
	}
	bashModDir = strings.TrimSpace(string(modOut))
	return realGo, realGoroot, signalUnixBytes, bashModDir
}

func writeFakeSDK(t *testing.T, sdkRoot string, signalBytes []byte, goShimBin string, readOnly bool) {
	t.Helper()
	runtimeDir := filepath.Join(sdkRoot, "src", "runtime")
	binDir := filepath.Join(sdkRoot, "bin")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(runtimeDir, "signal_unix.go")
	if err := os.WriteFile(sigPath, signalBytes, 0644); err != nil {
		t.Fatal(err)
	}
	shimBytes, err := os.ReadFile(goShimBin)
	if err != nil {
		t.Fatal(err)
	}
	goName := "go"
	if runtime.GOOS == "windows" {
		goName += ".exe"
	}
	dstGo := filepath.Join(binDir, goName)
	if err := os.WriteFile(dstGo, shimBytes, 0755); err != nil {
		t.Fatal(err)
	}
	if readOnly && runtime.GOOS != "windows" {
		if err := os.Chmod(sigPath, 0444); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{runtimeDir, filepath.Join(sdkRoot, "src"), binDir, sdkRoot} {
			if err := os.Chmod(d, 0555); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			for _, d := range []string{sdkRoot, binDir, filepath.Join(sdkRoot, "src"), runtimeDir} {
				_ = os.Chmod(d, 0755)
			}
			_ = os.Chmod(sigPath, 0644)
		})
	}
}

type productCapture struct {
	overlayBytes []byte
	signalBytes  []byte
	args         []string
	replaceMap   map[string]string
}

func readCapture(t *testing.T, capDir string) productCapture {
	t.Helper()
	var c productCapture
	var err error
	c.overlayBytes, err = os.ReadFile(filepath.Join(capDir, "overlay.json"))
	if err != nil {
		t.Fatalf("read captured overlay.json: %v", err)
	}
	c.signalBytes, err = os.ReadFile(filepath.Join(capDir, "signal_unix.go"))
	if err != nil {
		t.Fatalf("read captured signal_unix.go: %v", err)
	}
	argsBytes, err := os.ReadFile(filepath.Join(capDir, "args.json"))
	if err != nil {
		t.Fatalf("read captured args.json: %v", err)
	}
	if err := json.Unmarshal(argsBytes, &c.args); err != nil {
		t.Fatalf("unmarshal args.json: %v", err)
	}
	var parsed struct {
		Replace map[string]string
	}
	if err := json.Unmarshal(c.overlayBytes, &parsed); err != nil {
		t.Fatalf("unmarshal overlay.json: %v\nraw: %s", err, c.overlayBytes)
	}
	c.replaceMap = parsed.Replace
	return c
}

func filterEnv(base []string, dropKeys ...string) []string {
	drop := map[string]bool{}
	for _, k := range dropKeys {
		drop[strings.ToUpper(k)] = true
	}
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(k)] {
			out = append(out, kv)
		}
	}
	return out
}

// TestProductGoByteIdenticalToGoReference verifies that for go1.27.4,
// cmd/bsh/productgo.bsh emits a patched signal_unix.go and overlay.json that
// are byte-identical to the Go productgo implementation, under both bsh and
// released bashy --bashsharp, for SDKs both outside and inside GOMODCACHE.
func TestProductGoByteIdenticalToGoReference(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix runtime overlay comparison")
	}
	realGo, _, origSignalBytes, bashModDir := requireToolchainGo1274(t)
	bshBin := buildTestBsh(t)
	goShim := buildRecordingGoShim(t)
	refProductGoBin := buildRefGoProductGo(t, realGo, bashModDir)

	type runnerSpec struct {
		name string
		bin  string
		args []string
	}
	runners := []runnerSpec{
		{name: "bsh", bin: bshBin, args: []string{"./productgo.bsh"}},
	}
	if bashyBin, err := exec.LookPath("bashy"); err == nil {
		runners = append(runners, runnerSpec{name: "bashy", bin: bashyBin, args: []string{"--bashsharp", "./productgo.bsh"}})
	}

	for _, inModcache := range []bool{false, true} {
		layout := "outside-modcache"
		if inModcache {
			layout = "inside-modcache"
		}
		t.Run(layout, func(t *testing.T) {
			rootDir := t.TempDir()
			modCache := filepath.Join(rootDir, "gomodcache")
			if err := os.MkdirAll(modCache, 0755); err != nil {
				t.Fatal(err)
			}
			sdkRoot := filepath.Join(rootDir, "sdk-installed")
			if inModcache {
				sdkRoot = filepath.Join(modCache, "golang.org", "toolchain@v0.0.1-go1.27.4."+runtime.GOOS+"-"+runtime.GOARCH)
			}
			writeFakeSDK(t, sdkRoot, origSignalBytes, goShim, inModcache)
			origInfo, err := os.Stat(filepath.Join(sdkRoot, "src", "runtime", "signal_unix.go"))
			if err != nil {
				t.Fatal(err)
			}

			// 1. Capture reference output from the Go productgo implementation.
			goCapDir := filepath.Join(rootDir, "cap-go")
			if err := os.MkdirAll(goCapDir, 0700); err != nil {
				t.Fatal(err)
			}
			goTmp := filepath.Join(rootDir, "go-tmp")
			if err := os.MkdirAll(goTmp, 0700); err != nil {
				t.Fatal(err)
			}
			baseEnv := filterEnv(os.Environ(),
				"BASHY_EXE", "GOROOT", "GOTOOLCHAIN", "GOMODCACHE", "GOOS", "GOARCH", "GOFLAGS", "BASHY_PRODUCTGO_CACHE", "TMPDIR", "PATH")
			goEnv := append(append([]string{}, baseEnv...),
				"GOWORK=off",
				"PATH="+filepath.Join(sdkRoot, "bin"),
				"TMPDIR="+goTmp,
				"FAKE_GO_REAL_BIN="+realGo,
				"FAKE_GOROOT="+sdkRoot,
				"FAKE_GOVERSION=go1.27.4",
				"FAKE_GOOS="+runtime.GOOS,
				"FAKE_GOMODCACHE="+modCache,
				"FAKE_CAPTURE_DIR="+goCapDir,
			)
			outBinPath := filepath.Join(rootDir, "out.bin")
			goCmd := exec.Command(refProductGoBin, runtime.GOOS, runtime.GOARCH, "", "build", "-tags", "extra_tag", "-ldflags", "-s -w", "-o", outBinPath, ".")
			goCmd.Env = goEnv
			if out, err := goCmd.CombinedOutput(); err != nil {
				t.Fatalf("reference Go productgo failed: %v\n%s", err, out)
			}
			goCap := readCapture(t, goCapDir)
			if len(goCap.replaceMap) != 1 {
				t.Fatalf("Go productgo replace entries = %d, want 1", len(goCap.replaceMap))
			}
			var goSrcKey, goReplVal string
			for k, v := range goCap.replaceMap {
				goSrcKey, goReplVal = k, v
			}

			// 2. Run productgo.bsh under each bootstrap runner and compare byte-for-byte.
			for _, runner := range runners {
				t.Run(runner.name, func(t *testing.T) {
					bshCapDir := filepath.Join(rootDir, "cap-"+runner.name)
					if err := os.MkdirAll(bshCapDir, 0700); err != nil {
						t.Fatal(err)
					}
					cacheDir := filepath.Join(rootDir, "cache-"+runner.name)
					cmdArgs := append(append([]string{}, runner.args...), "build", "-tags", "extra_tag", "-ldflags", "-s -w", "-o", outBinPath, ".")
					cmd := exec.Command(runner.bin, cmdArgs...)
					cmd.Env = append(append([]string{}, baseEnv...),
						"GOWORK=off",
						"PATH="+filepath.Join(sdkRoot, "bin"),
						"BASHY_PRODUCTGO_CACHE="+cacheDir,
						"FAKE_GO_REAL_BIN="+realGo,
						"FAKE_GOROOT="+sdkRoot,
						"FAKE_GOVERSION=go1.27.4",
						"FAKE_GOOS="+runtime.GOOS,
						"FAKE_GOMODCACHE="+modCache,
						"FAKE_CAPTURE_DIR="+bshCapDir,
					)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%s productgo.bsh failed: %v\n%s", runner.name, err, out)
					}
					bshCap := readCapture(t, bshCapDir)

					// Byte-identical patched signal_unix.go (fail-closed).
					if !bytes.Equal(bshCap.signalBytes, goCap.signalBytes) {
						t.Fatalf("%s patched signal_unix.go differs from Go productgo (got %d bytes, want %d bytes)",
							runner.name, len(bshCap.signalBytes), len(goCap.signalBytes))
					}

					// Byte-identical overlay.json structure & encoding vs Go json.Marshal AND Go productgo.
					if len(bshCap.replaceMap) != 1 {
						t.Fatalf("%s replace entries = %d, want 1", runner.name, len(bshCap.replaceMap))
					}
					var bshSrcKey, bshReplVal string
					for k, v := range bshCap.replaceMap {
						bshSrcKey, bshReplVal = k, v
					}
					wantOverlayBytes, err := json.Marshal(map[string]any{
						"Replace": map[string]string{bshSrcKey: bshReplVal},
					})
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(bshCap.overlayBytes, wantOverlayBytes) {
						t.Fatalf("%s overlay.json is not byte-identical to Go json.Marshal:\ngot:  %q\nwant: %q",
							runner.name, bshCap.overlayBytes, wantOverlayBytes)
					}
					// Substitute the per-run root/replacement paths in Go productgo's raw overlay.json
					// and verify 100% byte equality of the JSON framing, key order, and escaping.
					goNormalized := bytes.ReplaceAll(goCap.overlayBytes, mustJSONStringContent(t, goSrcKey), mustJSONStringContent(t, bshSrcKey))
					goNormalized = bytes.ReplaceAll(goNormalized, mustJSONStringContent(t, goReplVal), mustJSONStringContent(t, bshReplVal))
					if !bytes.Equal(bshCap.overlayBytes, goNormalized) {
						t.Fatalf("%s overlay.json differs from Go productgo raw bytes:\ngot:  %q\nwant: %q",
							runner.name, bshCap.overlayBytes, goNormalized)
					}
					if !inModcache && bshSrcKey != goSrcKey {
						t.Fatalf("outside-modcache source key = %q, want %q", bshSrcKey, goSrcKey)
					}
					if inModcache && strings.HasPrefix(bshSrcKey, modCache) {
						t.Fatalf("inside-modcache build root was not relocated out of GOMODCACHE: %q", bshSrcKey)
					}

					// Verify build flags match Go productgo (excluding the overlay file path itself).
					if got, want := stripOverlayArg(bshCap.args), stripOverlayArg(goCap.args); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
						t.Fatalf("%s go build args = %q, want %q", runner.name, got, want)
					}

					// Verify warm-cache determinism: second run must keep overlay mtime unchanged.
					var bshOverlayPath string
					for _, a := range bshCap.args {
						if v, ok := strings.CutPrefix(a, "-overlay="); ok {
							bshOverlayPath = v
						}
					}
					st1, err := os.Stat(bshOverlayPath)
					if err != nil {
						t.Fatal(err)
					}
					time.Sleep(20 * time.Millisecond)
					cmd2 := exec.Command(runner.bin, cmdArgs...)
					cmd2.Env = cmd.Env
					if out, err := cmd2.CombinedOutput(); err != nil {
						t.Fatalf("second %s run failed: %v\n%s", runner.name, err, out)
					}
					st2, err := os.Stat(bshOverlayPath)
					if err != nil {
						t.Fatal(err)
					}
					if !st1.ModTime().Equal(st2.ModTime()) {
						t.Fatalf("warm cache rewrote overlay.json: %v vs %v", st1.ModTime(), st2.ModTime())
					}
				})
			}

			// Shared SDK source must remain completely untouched.
			afterBytes, err := os.ReadFile(filepath.Join(sdkRoot, "src", "runtime", "signal_unix.go"))
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(filepath.Join(sdkRoot, "src", "runtime", "signal_unix.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(origSignalBytes, afterBytes) || afterInfo.Mode() != origInfo.Mode() || !afterInfo.ModTime().Equal(origInfo.ModTime()) {
				t.Fatal("shared SDK signal_unix.go was mutated")
			}
		})
	}
}

func mustJSONStringContent(t *testing.T, s string) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b[1 : len(b)-1]
}

func stripOverlayArg(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-overlay=") {
			out = append(out, "-overlay=<path>")
		} else {
			out = append(out, a)
		}
	}
	return out
}

// TestProductGoFailClosedGuards verifies fail-closed rejection on wrong Go
// version, missing/duplicate runtime patch anchor, unrecognized signal_unix.go
// hash, tampered cached SDK, symlinks in managed SDK, cache inside GOMODCACHE,
// -trimpath opt-outs, contract overrides, and non-build subcommands.
func TestProductGoFailClosedGuards(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix runtime overlay guards")
	}
	realGo, _, origSignalBytes, _ := requireToolchainGo1274(t)
	bshBin := buildTestBsh(t)
	goShim := buildRecordingGoShim(t)

	anchor := "\tt := &sigtable[sig]\n\tif t.flags&_SigSetStack != 0 {"
	if strings.Count(string(origSignalBytes), anchor) != 1 {
		t.Fatal("expected single anchor in go1.27.4 signal_unix.go")
	}

	runWithSDK := func(t *testing.T, goversion string, signalContent []byte, extraEnv []string, args ...string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		sdkRoot := filepath.Join(dir, "sdk")
		modCache := filepath.Join(dir, "modcache")
		cacheDir := filepath.Join(dir, "cache")
		writeFakeSDK(t, sdkRoot, signalContent, goShim, false)
		baseEnv := filterEnv(os.Environ(),
			"BASHY_EXE", "GOROOT", "GOTOOLCHAIN", "GOMODCACHE", "GOOS", "GOARCH", "GOFLAGS", "BASHY_PRODUCTGO_CACHE", "PATH")
		env := append(baseEnv,
			"GOWORK=off",
			"PATH="+filepath.Join(sdkRoot, "bin"),
			"BASHY_PRODUCTGO_CACHE="+cacheDir,
			"FAKE_GO_REAL_BIN="+realGo,
			"FAKE_GOROOT="+sdkRoot,
			"FAKE_GOVERSION="+goversion,
			"FAKE_GOOS="+runtime.GOOS,
			"FAKE_GOMODCACHE="+modCache,
		)
		env = append(env, extraEnv...)
		cmd := exec.Command(bshBin, append([]string{"./productgo.bsh"}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("wrong-goversion", func(t *testing.T) {
		out, err := runWithSDK(t, "go1.27.2", origSignalBytes, nil, "build", ".")
		if err == nil || !strings.Contains(out, "requires go1.27.4, found go1.27.2") {
			t.Fatalf("expected wrong-version rejection, got err=%v out=%q", err, out)
		}
	})

	t.Run("missing-anchor", func(t *testing.T) {
		noAnchor := []byte(strings.Replace(string(origSignalBytes), anchor, "\tt := &sigtable[sig]\n\tif false {", 1))
		out, err := runWithSDK(t, "go1.27.4", noAnchor, nil, "build", ".")
		if err == nil || !strings.Contains(out, "anchor not unique") {
			t.Fatalf("expected missing-anchor rejection, got err=%v out=%q", err, out)
		}
	})

	t.Run("duplicate-anchor", func(t *testing.T) {
		dupAnchor := []byte(strings.Replace(string(origSignalBytes), anchor, anchor+"\n"+anchor, 1))
		out, err := runWithSDK(t, "go1.27.4", dupAnchor, nil, "build", ".")
		if err == nil || !strings.Contains(out, "runtime patch anchor not unique") {
			t.Fatalf("expected duplicate-anchor rejection, got err=%v out=%q", err, out)
		}
	})

	t.Run("unrecognized-hash-with-single-anchor", func(t *testing.T) {
		wrongHash := append(append([]byte{}, origSignalBytes...), []byte("\n// extra trailing comment\n")...)
		out, err := runWithSDK(t, "go1.27.4", wrongHash, nil, "build", ".")
		if err == nil || !strings.Contains(out, "unrecognized Go runtime source") {
			t.Fatalf("expected unrecognized-source rejection, got err=%v out=%q", err, out)
		}
	})

	t.Run("trimpath-optout", func(t *testing.T) {
		for _, bad := range []string{"-trimpath=false", "-trimpath=0", "-trimpath=F", "-trimpath=invalid"} {
			out, err := runWithSDK(t, "go1.27.4", origSignalBytes, nil, "build", bad, ".")
			if err == nil || !strings.Contains(out, "require -trimpath") {
				t.Fatalf("flag %s: expected trimpath rejection, got err=%v out=%q", bad, err, out)
			}
			outEnv, errEnv := runWithSDK(t, "go1.27.4", origSignalBytes, []string{"GOFLAGS='" + bad + "'"}, "build", ".")
			if errEnv == nil || !strings.Contains(outEnv, "require -trimpath") {
				t.Fatalf("GOFLAGS %s: expected trimpath rejection, got err=%v out=%q", bad, errEnv, outEnv)
			}
		}
		// strconv.ParseBool's true spellings stay accepted, as in the Go productgo.
		for _, good := range []string{"-trimpath=true", "-trimpath=1", "-trimpath=T"} {
			if out, err := runWithSDK(t, "go1.27.4", origSignalBytes, nil, "build", good, "."); err != nil {
				t.Fatalf("flag %s: rejected: %v\n%s", good, err, out)
			}
		}
	})

	t.Run("contract-and-flag-overrides", func(t *testing.T) {
		for _, tc := range []struct {
			extraEnv []string
			args     []string
			wantSub  string
		}{
			{nil, []string{"build", "-ldflags=-X runtime.bashyInheritedIgnore=0", "."}, "cannot override"},
			{nil, []string{"build", "-overlay=custom.json", "."}, "caller overlay unsupported"},
			{[]string{"GOFLAGS=-tags=foo"}, []string{"build", "."}, "GOFLAGS overlay/ldflags/tags unsupported"},
			{nil, []string{"test", "."}, "only product builds"},
			{nil, []string{"run", "."}, "only product builds"},
		} {
			out, err := runWithSDK(t, "go1.27.4", origSignalBytes, tc.extraEnv, tc.args...)
			if err == nil || !strings.Contains(out, tc.wantSub) {
				t.Fatalf("args=%q env=%q: want %q, got err=%v out=%q", tc.args, tc.extraEnv, tc.wantSub, err, out)
			}
		}
	})

	t.Run("cache-inside-gomodcache-and-tampered-sdk", func(t *testing.T) {
		dir := t.TempDir()
		modCache := filepath.Join(dir, "modcache")
		sdkRoot := filepath.Join(modCache, "sdk")
		writeFakeSDK(t, sdkRoot, origSignalBytes, goShim, false)

		// Cache inside GOMODCACHE must fail closed.
		baseEnv := filterEnv(os.Environ(),
			"BASHY_EXE", "GOROOT", "GOTOOLCHAIN", "GOMODCACHE", "GOOS", "GOARCH", "GOFLAGS", "BASHY_PRODUCTGO_CACHE", "PATH")
		badCmd := exec.Command(bshBin, "./productgo.bsh", "build", ".")
		badCmd.Env = append(append([]string{}, baseEnv...),
			"PATH="+filepath.Join(sdkRoot, "bin"),
			"BASHY_PRODUCTGO_CACHE="+filepath.Join(modCache, "subcache"),
			"FAKE_GOROOT="+sdkRoot,
			"FAKE_GOVERSION=go1.27.4",
			"FAKE_GOOS="+runtime.GOOS,
			"FAKE_GOMODCACHE="+modCache,
		)
		if out, err := badCmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "outside GOMODCACHE") {
			t.Fatalf("accepted cache inside GOMODCACHE: err=%v out=%q", err, out)
		}

		// Populate relocated SDK cache, then tamper with cached signal_unix.go and confirm fail-closed.
		goodCache := filepath.Join(dir, "good-cache")
		goodEnv := append(append([]string{}, baseEnv...),
			"PATH="+filepath.Join(sdkRoot, "bin"),
			"BASHY_PRODUCTGO_CACHE="+goodCache,
			"FAKE_GOROOT="+sdkRoot,
			"FAKE_GOVERSION=go1.27.4",
			"FAKE_GOOS="+runtime.GOOS,
			"FAKE_GOMODCACHE="+modCache,
		)
		okCmd := exec.Command(bshBin, "./productgo.bsh", "build", ".")
		okCmd.Env = goodEnv
		if out, err := okCmd.CombinedOutput(); err != nil {
			t.Fatalf("initial cached SDK build failed: %v\n%s", err, out)
		}
		matches, err := filepath.Glob(filepath.Join(goodCache, "sdk-*", "goroot", "src", "runtime", "signal_unix.go"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate cached signal_unix.go: %v %v", matches, err)
		}
		if err := os.WriteFile(matches[0], []byte("tampered"), 0600); err != nil {
			t.Fatal(err)
		}
		tamperCmd := exec.Command(bshBin, "./productgo.bsh", "build", ".")
		tamperCmd.Env = goodEnv
		if out, err := tamperCmd.CombinedOutput(); err == nil {
			t.Fatalf("tampered cached SDK was accepted: %s", out)
		}
	})

	t.Run("existing-cache-dir-kept-and-no-staging-left", func(t *testing.T) {
		dir := t.TempDir()
		modCache := filepath.Join(dir, "modcache")
		sdkRoot := filepath.Join(modCache, "sdk")
		writeFakeSDK(t, sdkRoot, origSignalBytes, goShim, false)
		cacheDir := filepath.Join(dir, "shared-cache")
		if err := os.Mkdir(cacheDir, 0755); err != nil {
			t.Fatal(err)
		}
		baseEnv := filterEnv(os.Environ(),
			"BASHY_EXE", "GOROOT", "GOTOOLCHAIN", "GOMODCACHE", "GOOS", "GOARCH", "GOFLAGS", "BASHY_PRODUCTGO_CACHE", "PATH")
		for i := 0; i < 2; i++ {
			cmd := exec.Command(bshBin, "./productgo.bsh", "build", ".")
			cmd.Env = append(append([]string{}, baseEnv...),
				"PATH="+filepath.Join(sdkRoot, "bin"),
				"BASHY_PRODUCTGO_CACHE="+cacheDir,
				"FAKE_GOROOT="+sdkRoot,
				"FAKE_GOVERSION=go1.27.4",
				"FAKE_GOOS="+runtime.GOOS,
				"FAKE_GOMODCACHE="+modCache,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build %d with existing cache dir failed: %v\n%s", i, err, out)
			}
		}
		// A caller-supplied cache directory keeps its mode.
		info, err := os.Stat(cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0755 {
			t.Fatalf("existing cache dir re-moded to %v, want 0755", info.Mode().Perm())
		}
		entries, err := os.ReadDir(cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) != 2 || !strings.HasPrefix(names[0], "overlay-") || !strings.HasPrefix(names[1], "sdk-") {
			t.Fatalf("cache entries = %q, want one overlay-<key> and one sdk-<key>", names)
		}
		// The published SDK holds exactly the copied toolchain, never a nested stage.
		sdkEntries, err := os.ReadDir(filepath.Join(cacheDir, names[1]))
		if err != nil || len(sdkEntries) != 1 || sdkEntries[0].Name() != "goroot" {
			t.Fatalf("published sdk entries = %v err=%v, want only goroot", sdkEntries, err)
		}
	})

	t.Run("managed-go-via-BASHY_EXE-without-PATH", func(t *testing.T) {
		dir := t.TempDir()
		sdkRoot := filepath.Join(dir, "sdk")
		writeFakeSDK(t, sdkRoot, origSignalBytes, goShim, false)
		front := filepath.Join(dir, "managed-front")
		script := "#!/bin/sh\n[ \"$1\" = go ] || exit 91\nshift\nexec '" + filepath.Join(sdkRoot, "bin", "go") + "' \"$@\"\n"
		if err := os.WriteFile(front, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		capDir := filepath.Join(dir, "cap")
		if err := os.MkdirAll(capDir, 0700); err != nil {
			t.Fatal(err)
		}
		baseEnv := filterEnv(os.Environ(),
			"BASHY_EXE", "GOROOT", "GOTOOLCHAIN", "GOMODCACHE", "GOOS", "GOARCH", "GOFLAGS", "BASHY_PRODUCTGO_CACHE", "PATH")
		cmd := exec.Command(bshBin, "./productgo.bsh", "build", "-o", filepath.Join(dir, "out"), ".")
		cmd.Env = append(baseEnv,
			"PATH="+filepath.Join(dir, "empty-path"),
			"BASHY_EXE="+front,
			"BASHY_PRODUCTGO_CACHE="+filepath.Join(dir, "cache"),
			"FAKE_GOROOT="+sdkRoot,
			"FAKE_GOVERSION=go1.27.4",
			"FAKE_GOOS="+runtime.GOOS,
			"FAKE_GOMODCACHE="+filepath.Join(dir, "modcache"),
			"FAKE_CAPTURE_DIR="+capDir,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("BASHY_EXE managed build with empty PATH failed: %v\n%s", err, out)
		}
	})
}

// TestProductGoWindowsNoOverlay verifies that GOOS=windows uses no Unix
// runtime overlay (matching Go productgo) and builds cmd/bsh cleanly.
func TestProductGoWindowsNoOverlay(t *testing.T) {
	realGo, _, origSignalBytes, _ := requireToolchainGo1274(t)
	bshBin := buildTestBsh(t)
	goShim := buildRecordingGoShim(t)

	// 1. Fast shim verification: GOOS=windows passes -trimpath with zero -overlay/-tags/-ldflags injection.
	dir := t.TempDir()
	sdkRoot := filepath.Join(dir, "sdk")
	capDir := filepath.Join(dir, "cap")
	if err := os.MkdirAll(capDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFakeSDK(t, sdkRoot, origSignalBytes, goShim, false)
	baseEnv := filterEnv(os.Environ(),
		"BASHY_EXE", "GOROOT", "GOTOOLCHAIN", "GOMODCACHE", "GOOS", "GOARCH", "GOFLAGS", "BASHY_PRODUCTGO_CACHE", "PATH")
	shimCmd := exec.Command(bshBin, "./productgo.bsh", "build", "-o", filepath.Join(dir, "bsh.exe"), ".")
	shimCmd.Env = append(baseEnv,
		"GOOS=windows",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
		"PATH="+filepath.Join(sdkRoot, "bin"),
		"FAKE_GO_REAL_BIN="+realGo,
		"FAKE_GOROOT="+sdkRoot,
		"FAKE_GOVERSION=go1.27.4",
		"FAKE_GOMODCACHE="+filepath.Join(dir, "modcache"),
		"FAKE_CAPTURE_DIR="+capDir,
	)
	if out, err := shimCmd.CombinedOutput(); err != nil {
		t.Fatalf("windows shim build failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(capDir, "overlay.json")); !os.IsNotExist(err) {
		t.Fatalf("GOOS=windows unexpectedly emitted overlay.json")
	}
	argsBytes, err := os.ReadFile(filepath.Join(capDir, "args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	if err := json.Unmarshal(argsBytes, &gotArgs); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"build", "-trimpath", "-o", filepath.Join(dir, "bsh.exe"), "."}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("GOOS=windows args = %q, want %q", gotArgs, wantArgs)
	}

	// 2. Real Windows cross-build of cmd/bsh when not in -short mode.
	if testing.Short() {
		return
	}
	winExe := filepath.Join(dir, "real-bsh.exe")
	winBuild := exec.Command(bshBin, "./productgo.bsh", "build", "-mod=readonly", "-o", winExe, ".")
	winBuild.Env = append(standaloneEnv(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := winBuild.CombinedOutput(); err != nil {
		t.Fatalf("real GOOS=windows productgo.bsh build failed: %v\n%s", err, out)
	}
	peBytes, err := os.ReadFile(winExe)
	if err != nil || len(peBytes) < 2 || string(peBytes[:2]) != "MZ" {
		t.Fatalf("expected Windows PE executable (MZ header), err=%v len=%d", err, len(peBytes))
	}
}
