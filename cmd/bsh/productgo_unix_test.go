//go:build linux || darwin

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBshProductInheritedIgnoredSignals builds bsh via cmd/bsh/productgo.bsh
// and verifies that inherited SIG_IGN dispositions survive Go runtime startup
// across exec in both bsh and sh routes.
func TestBshProductInheritedIgnoredSignals(t *testing.T) {
	if testing.Short() {
		t.Skip("builds product bsh executable")
	}
	requireToolchainGo1274(t)
	bootstrapBsh := buildTestBsh(t)

	dir := t.TempDir()
	productBsh := filepath.Join(dir, "bsh")
	cacheDir := filepath.Join(dir, "productgo-cache")
	build := exec.Command(bootstrapBsh, "./productgo.bsh", "build", "-mod=readonly",
		"-ldflags", "-s -w -X 'github.com/bashsharp/bashsharp/cli.bashVersion=5.3.0(1)-bsh-test'",
		"-o", productBsh, ".")
	build.Env = append(standaloneEnv(), "CGO_ENABLED=0", "BASHY_PRODUCTGO_CACHE="+cacheDir)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("productgo.bsh build bsh: %v\n%s", err, out)
	}

	shLink := filepath.Join(dir, "sh")
	if err := os.Symlink(productBsh, shLink); err != nil {
		t.Fatal(err)
	}

	verCmd := exec.Command(productBsh, "--version")
	verCmd.Env = []string{"HOME=" + dir, "PATH=" + dir + "/empty"}
	if out, err := verCmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "5.3.0(1)-bsh-test") {
		t.Fatalf("product bsh --version: err=%v out=%q", err, out)
	}

	for _, entry := range []struct {
		route string
		bin   string
	}{
		{"bsh", productBsh},
		{"sh", shLink},
	} {
		t.Run(entry.route, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				sig  syscall.Signal
			}{
				{"ABRT", syscall.SIGABRT},
				{"ALRM", syscall.SIGALRM},
				{"PIPE", syscall.SIGPIPE},
				{"QUIT", syscall.SIGQUIT},
				{"TERM", syscall.SIGTERM},
				{"USR1", syscall.SIGUSR1},
				{"USR2", syscall.SIGUSR2},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cmd := exec.Command("/bin/sh", "-c", "ulimit -c 0; trap '' "+tc.name+`; exec "$TEST_SHELL"`)
					cmd.Env = append(os.Environ(), "TEST_SHELL="+entry.bin, "PATH=/usr/bin:/bin")
					stdin, err := cmd.StdinPipe()
					if err != nil {
						t.Fatal(err)
					}
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					if err := cmd.Start(); err != nil {
						t.Fatal(err)
					}
					time.Sleep(150 * time.Millisecond)
					sigErr := cmd.Process.Signal(tc.sig)
					_, writeErr := io.WriteString(stdin, "printf test_string\n")
					_ = stdin.Close()
					waitErr := cmd.Wait()
					if sigErr != nil || writeErr != nil || waitErr != nil || stdout.String() != "test_string" {
						t.Fatalf("route=%s sig=%s: signal=%v write=%v wait=%v stdout=%q stderr=%q",
							entry.route, tc.name, sigErr, writeErr, waitErr, stdout.String(), stderr.String())
					}
				})
			}
		})
	}
}
