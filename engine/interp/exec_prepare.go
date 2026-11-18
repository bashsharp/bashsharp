package interp

import (
	"io"
	"os"
	"strings"
)

// parseShebang implements bash's one-interpreter-argument shebang parsing.
func parseShebang(data []byte) (interp, optarg string, ok bool) {
	if len(data) < 2 || data[0] != '#' || data[1] != '!' {
		return "", "", false
	}
	line := string(data[2:])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSuffix(line, "\r")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	// Like the Linux kernel (and bash's shell_execve fallback on hosts
	// without one), everything after the interpreter is ONE argument:
	// `#!/usr/bin/env python -u` passes "python -u".
	interp, rest, _ := strings.Cut(line, " ")
	if i := strings.IndexAny(interp, " \t"); i >= 0 {
		interp = interp[:i]
	}
	optarg = strings.TrimSpace(rest)
	return interp, optarg, true
}

// shebangProbeSize is how much of a file the shebang probe reads: the
// interpreter line is at the start, and the file may be a 100 MB binary.
const shebangProbeSize = 512

// readShebangProbe returns the first shebangProbeSize bytes of path.
func readShebangProbe(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, shebangProbeSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

func shebangArgs(interp, optarg, script string, args []string) []string {
	out := []string{interp}
	if optarg != "" {
		out = append(out, optarg)
	}
	out = append(out, script)
	if len(args) > 1 {
		out = append(out, args[1:]...)
	}
	return out
}
