package interp

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/qiangli/bash/interp/ownedexec"
)

type ownedExecHandoff struct {
	file    *os.File
	path    string
	apply   func(*exec.Cmd)
	started func()
}

func (h *ownedExecHandoff) close() {
	if h == nil {
		return
	}
	if h.started != nil {
		h.started()
		h.started = nil
	}
	_ = h.file.Close()
	if h.path != "" {
		_ = os.Remove(h.path)
	}
}

func (r *Runner) ownsExecutable(path string) bool {
	if r == nil || len(r.ownedExecPaths) == 0 {
		return false
	}
	candidate, err := os.Stat(path)
	if err != nil || !candidate.Mode().IsRegular() {
		return false
	}
	var candidateHash [32]byte
	var candidateHashErr error
	hashed := false
	for _, owned := range r.ownedExecPaths {
		info, err := os.Stat(owned)
		if err == nil && os.SameFile(candidate, info) {
			return true
		}
		if err == nil && info.Mode().IsRegular() && info.Size() == candidate.Size() {
			if !hashed {
				candidateHash, candidateHashErr = executableContentHash(path)
				hashed = true
			}
			ownedHash, ownedErr := executableContentHash(owned)
			if candidateHashErr == nil && ownedErr == nil && candidateHash == ownedHash {
				// The fixture builder copies applets when hard-link creation is
				// unavailable. Exact content equality still verifies ownership.
				return true
			}
		}
	}
	return false
}

// ownedExecNeeded leaves ordinary launches on their native fast path. The
// margin accounts for Windows quoting and Linux's per-string page ceiling.
func ownedExecNeeded(args, env []string) bool {
	argBytes, envBytes := 0, 0
	for _, s := range args {
		argBytes += len(s) + 1
		if len(s) > 64<<10 {
			return true
		}
	}
	for _, s := range env {
		envBytes += len(s) + 1
		if runtime.GOOS != "windows" && len(s) > 64<<10 {
			return true
		}
	}
	if runtime.GOOS == "windows" {
		return argBytes > 8<<10
	}
	return argBytes+envBytes > 96<<10
}

// prepareOwnedExec replaces argv with a short private invocation and leaves
// only env strings that the native Unix exec layer can safely carry. The child
// reconstitutes the complete original environment before command dispatch.
func prepareOwnedExec(r *Runner, path string, args, env []string, extraFiles []*os.File) (*ownedExecHandoff, []string, []string, []*os.File, error) {
	if !ownedExecNeeded(args, env) || !r.ownsExecutable(path) {
		return nil, args, env, extraFiles, nil
	}
	f, err := os.CreateTemp("", ".bashy-owned-exec-*")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	h := &ownedExecHandoff{file: f, path: f.Name()}
	frame := ownedexec.Frame{Args: args}
	nativeEnv := append([]string(nil), env...)
	if runtime.GOOS != "windows" {
		frame.Env = env
		// Keep startup controls visible to the Go runtime, loader and package
		// initializers, even when earlier ordinary variables fill the native
		// block. The full original environment is restored before main.
		nativeEnv = nativeEnv[:0]
		bytes := 0
		for pass := 0; pass < 2; pass++ {
			for _, entry := range env {
				if earlyNativeEnv(entry) != (pass == 0) {
					continue
				}
				if len(entry) > 64<<10 || bytes+len(entry)+1 > 64<<10 {
					continue
				}
				nativeEnv = append(nativeEnv, entry)
				bytes += len(entry) + 1
			}
		}
	}
	if runtime.GOOS == "windows" {
		for _, entry := range env {
			if len(entry) >= len(ownedexec.Marker)+1 && entry[:len(ownedexec.Marker)+1] == ownedexec.Marker+"=" {
				frame.Env = append(frame.Env, entry)
			}
		}
	}
	if err := ownedexec.Write(f, frame); err != nil {
		h.close()
		return nil, nil, nil, nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		h.close()
		return nil, nil, nil, nil, err
	}
	handle, apply, started, err := ownedExecPlatform(f, len(extraFiles))
	if err != nil {
		h.close()
		return nil, nil, nil, nil, err
	}
	h.apply, h.started = apply, started
	nativeEnv = setExecEnvValue(nativeEnv, ownedexec.Marker, strconv.FormatUint(handle, 10))
	if runtime.GOOS != "windows" {
		extraFiles = append(extraFiles, f)
	}
	// The exact original argv[0] travels in the frame. The launch argv[0]
	// only needs to name the program while its startup adopts the frame.
	shortArgs := []string{path, ownedexec.Sentinel}
	return h, shortArgs, nativeEnv, extraFiles, nil
}

func earlyNativeEnv(entry string) bool {
	name, _, ok := strings.Cut(entry, "=")
	if !ok {
		return false
	}
	if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "XDG_") {
		return true
	}
	switch name {
	case "PATH", "HOME", "TMPDIR", "TZ", "LANG", "BASHY_ROOT":
		return true
	}
	return false
}
