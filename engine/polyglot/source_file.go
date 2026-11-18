package polyglot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// RunSourceFile runs a complete foreign-language source file with the same
// source-relative toolchain selection used by source fences. It returns the
// program's exit status; errors are reserved for setup and compilation
// failures, and always identify the source position and fence workaround.
func RunSourceFile(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	ext := strings.ToLower(filepath.Ext(path))
	language, fence := sourceFileLanguage(ext)
	if language == "" {
		return 2, fmt.Errorf("%s:1:1: unsupported source extension %s", path, ext)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 2, sourceFileError(path, fence, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return 2, sourceFileError(path, fence, err)
	}

	plan, err := DiscoverEnvironment(EnvironmentRequest{Language: language, Source: abs, Environ: os.Environ()})
	if err != nil {
		return 2, sourceFileError(path, fence, err)
	}

	ctx := context.Background()
	if language == "c" || language == "cpp" || language == "rust" {
		return compileAndRunSourceFile(ctx, path, abs, language, fence, plan, args, stdin, stdout, stderr)
	}
	cmdArgs := append([]string(nil), plan.ExecutableArgs...)
	switch language {
	case "typescript":
		cmdArgs = append(cmdArgs, abs)
	default: // Python runs on the resolved runtime.
		cmdArgs = append(cmdArgs, abs)
	}
	cmdArgs = append(cmdArgs, args...)
	return runSourceCommand(ctx, path, fence, plan, cmdArgs, stdin, stdout, stderr)
}

func sourceFileLanguage(ext string) (language, fence string) {
	switch ext {
	case ".c":
		return "c", "c"
	case ".cc", ".cpp", ".cxx":
		return "cpp", "cxx"
	case ".js", ".mjs", ".ts", ".tsx":
		return "typescript", "ts"
	case ".py":
		return "python", "py"
	case ".rs":
		return "rust", "rs"
	}
	return "", ""
}

func compileAndRunSourceFile(ctx context.Context, display, source, language, fence string, plan EnvironmentPlan, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	dir, err := os.MkdirTemp("", "bashsharp-source-")
	if err != nil {
		return 2, sourceFileError(display, fence, err)
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "program")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	compileArgs := append([]string(nil), plan.ExecutableArgs...)
	switch language {
	case "c":
		compileArgs = append(compileArgs, "-std=c17", "-o", output, source)
	case "cpp":
		compileArgs = append(compileArgs, "-std=c++20", "-o", output, source)
	case "rust":
		compileArgs = append(compileArgs, "-o", output, source)
	}
	compile := exec.CommandContext(ctx, plan.Executable, compileArgs...)
	compile.Dir, compile.Env = plan.Dir, append([]string(nil), plan.Env...)
	compile.Stdout, compile.Stderr = stdout, stderr
	// Compilation is deliberately not connected to the program's stdin.
	if err := compile.Run(); err != nil {
		return 2, sourceFileError(display, fence, fmt.Errorf("compile: %w", err))
	}
	runPlan := EnvironmentPlan{Executable: output, Dir: plan.Dir, Env: plan.Env}
	return runSourceCommand(ctx, display, fence, runPlan, args, stdin, stdout, stderr)
}

func runSourceCommand(ctx context.Context, display, fence string, plan EnvironmentPlan, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, plan.Executable, args...)
	cmd.Dir, cmd.Env = plan.Dir, append([]string(nil), plan.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 2, sourceFileError(display, fence, err)
	}
	return 0, nil
}

func sourceFileError(path, fence string, err error) error {
	return fmt.Errorf("%s:1:1: %v; use a ~~~%s fence in a .bsh script", path, err, fence)
}
