//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	coreutilsBinOnce sync.Once
	coreutilsBinPath string
	coreutilsBinErr  error
)

func builtCoreutilsBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("external umask regression builds cmd/coreutils; skipped with -short")
	}
	coreutilsBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bashy-coreutils-bin-")
		if err != nil {
			coreutilsBinErr = err
			return
		}
		coreutilsBinPath = filepath.Join(dir, "coreutils")
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			coreutilsBinErr = err
			return
		}
		// coreutils at the version bashy pins (the live tree in the dhnt
		// go.work), built in bashy's module so its fork replaces apply.
		build := exec.Command("go", "build", "-o", coreutilsBinPath, "github.com/qiangli/coreutils/cmd/coreutils")
		build.Dir = root
		out, err := build.CombinedOutput()
		if err != nil {
			coreutilsBinErr = fmt.Errorf("building cmd/coreutils: %v\n%s", err, out)
		}
	})
	if coreutilsBinErr != nil {
		t.Fatal(coreutilsBinErr)
	}
	return coreutilsBinPath
}

// Certification drives the lean cmd/bash binary and resolves touch as an
// external multicall command. Pin that exact boundary: each child inherits the
// Runner's current virtual umask, while the parent process's mask is restored.
func TestLeanBashExternalCoreutilsHonorsUmask(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(builtCoreutilsBin(t), filepath.Join(dir, "touch")); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "baseline")
	f, err := os.OpenFile(baseline, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	baseInfo, err := os.Stat(baseline)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(builtBashBin(t), "-c",
		"touch default; umask 077; touch restricted; umask 006; touch tp5")
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "PATH=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lean bash external touch: %v\n%s", err, out)
	}
	for name, want := range map[string]os.FileMode{
		"default":    baseInfo.Mode().Perm(),
		"restricted": 0o600,
		"tp5":        0o660,
	} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode=%#o, want %#o", name, got, want)
		}
	}
}
