package interp

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var runCompiledGoCommand = exec.Command

// RunCompiledGoFile runs a whole Go program with the toolchain selected for
// Bash# Go fences. Building first preserves the program's exit status (go run
// itself returns status 1 for any nonzero program exit).
func RunCompiledGoFile(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	identity, err := bashPPGoIdentity()
	if err != nil {
		return 2, err
	}
	root, err := os.MkdirTemp("", "bashsharp-go-*")
	if err != nil {
		return 2, err
	}
	defer os.RemoveAll(root)
	source, err := filepath.Abs(path)
	if err != nil {
		return 2, err
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return 2, err
	}
	if err := requireCompiledGoMain(source); err != nil {
		return 2, err
	}
	if !sourceInfo.IsDir() && strings.ToLower(filepath.Ext(source)) != ".go" {
		original, openErr := os.Open(source)
		if openErr != nil {
			return 2, openErr
		}
		defer original.Close()
		source = filepath.Join(root, "source.go")
		copyFile, createErr := os.OpenFile(source, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if createErr != nil {
			return 2, createErr
		}
		if _, err = io.Copy(copyFile, original); err != nil {
			copyFile.Close()
			return 2, err
		}
		if err = copyFile.Close(); err != nil {
			return 2, err
		}
	}
	program := filepath.Join(root, "program")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	build := runCompiledGoCommand(identity.Binary, "build", "-o", program, source)
	if moduleDir := nearestGoModuleDir(source, sourceInfo.IsDir()); moduleDir != "" {
		build.Dir = moduleDir
	}
	build.Stdout, build.Stderr = stdout, stderr
	if err = build.Run(); err != nil {
		return processStatus(err)
	}
	run := runCompiledGoCommand(program, args...)
	run.Stdin, run.Stdout, run.Stderr = stdin, stdout, stderr
	if err = run.Run(); err != nil {
		return processStatus(err)
	}
	return 0, nil
}

func requireCompiledGoMain(path string) error {
	fset := token.NewFileSet()
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".go") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			file, parseErr := parser.ParseFile(fset, filepath.Join(path, name), nil, parser.PackageClauseOnly)
			if parseErr != nil {
				continue
			}
			return compiledGoMainPackageError(fset, file)
		}
		return nil
	}
	file, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
	if err != nil {
		// Keep syntax/type diagnostics owned by the selected Go toolchain.
		return nil
	}
	return compiledGoMainPackageError(fset, file)
}

func compiledGoMainPackageError(fset *token.FileSet, file *ast.File) error {
	if file.Name.Name == "main" {
		return nil
	}
	pos := fset.Position(file.Package)
	return fmt.Errorf("%s: Go source package %s cannot run as a program; expose its exported functions from a ~~~go fence in a .bsh script", pos, file.Name.Name)
}

func nearestGoModuleDir(path string, isDir bool) string {
	dir := path
	if !isDir {
		dir = filepath.Dir(path)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func processStatus(err error) (int, error) {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 2, fmt.Errorf("run Go program: %w", err)
}
