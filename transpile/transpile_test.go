package transpile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/lower"
	"mvdan.cc/sh/v3/syntax"
)

func TestTranspileDispatchArgs(t *testing.T) {
	missingInput := filepath.Join(t.TempDir(), "does-not-exist.sh")
	_, missingErr := os.ReadFile(missingInput)
	if !os.IsNotExist(missingErr) {
		t.Fatalf("missing input fixture: got %v, want a file-not-found error", missingErr)
	}
	// Keep the actual file error in the diagnostic on every OS; Windows does
	// not use the Unix "no such file or directory" wording.
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing all",
			args:       []string{},
			wantExit:   2,
			wantStderr: "transpile: missing INPUT\n",
		},
		{
			name:       "missing bashpp",
			args:       []string{"input.sh", "-o", "out.go"},
			wantExit:   2,
			wantStderr: "transpile: --bashsharp is required\n",
		},
		{
			name:       "missing output",
			args:       []string{"--bashpp", "input.sh"},
			wantExit:   2,
			wantStderr: "transpile: missing -o OUTPUT.go\n",
		},
		{
			name:       "missing input",
			args:       []string{"--bashpp", "-o", "out.go"},
			wantExit:   2,
			wantStderr: "transpile: missing INPUT\n",
		},
		{
			name:       "file not found",
			args:       []string{"--bashpp", missingInput, "-o", "out.go"},
			wantExit:   2,
			wantStderr: "transpile: " + missingErr.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			exitCode := Main(tt.args)

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			io.Copy(&buf, r)
			stderr := buf.String()

			if exitCode != tt.wantExit {
				t.Errorf("got exit %d, want %d", exitCode, tt.wantExit)
			}
			if !strings.Contains(stderr, strings.TrimSpace(tt.wantStderr)) {
				t.Errorf("got stderr %q, want it to contain %q", stderr, tt.wantStderr)
			}
		})
	}
}

func TestTranspilePathCollisionsAndAliases(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "script.bpp")
	if err := os.WriteFile(srcFile, []byte("echo hi\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outFile := filepath.Join(dir, "out.go")

	// 1. input == output
	t.Run("input_equals_output", func(t *testing.T) {
		exit := Main([]string{"--bashpp", srcFile, "-o", srcFile})
		if exit != 2 {
			t.Errorf("got exit %d, want 2", exit)
		}
	})

	// 2. map == input
	t.Run("map_equals_input", func(t *testing.T) {
		exit := Main([]string{"--bashpp", srcFile, "-o", outFile, "--map", srcFile})
		if exit != 2 {
			t.Errorf("got exit %d, want 2", exit)
		}
	})

	// 3. map == output
	t.Run("map_equals_output", func(t *testing.T) {
		exit := Main([]string{"--bashpp", srcFile, "-o", outFile, "--map", outFile})
		if exit != 2 {
			t.Errorf("got exit %d, want 2", exit)
		}
	})

	// 4. symlink collision
	symFile := filepath.Join(dir, "symlink.bpp")
	if err := os.Symlink(srcFile, symFile); err == nil {
		t.Run("symlink_collision", func(t *testing.T) {
			exit := Main([]string{"--bashpp", srcFile, "-o", symFile})
			if exit != 2 {
				t.Errorf("got exit %d for symlink collision, want 2", exit)
			}
		})
	}

	// 5. hardlink collision
	hardFile := filepath.Join(dir, "hardlink.bpp")
	if err := os.Link(srcFile, hardFile); err == nil {
		t.Run("hardlink_collision", func(t *testing.T) {
			exit := Main([]string{"--bashpp", srcFile, "-o", hardFile})
			if exit != 2 {
				t.Errorf("got exit %d for hardlink collision, want 2", exit)
			}
		})
	}

	// 6. Destination directory collisions
	outDir := filepath.Join(dir, "out_dir")
	if err := os.Mkdir(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("output_is_directory", func(t *testing.T) {
		exit := Main([]string{"--bashpp", srcFile, "-o", outDir})
		if exit != 2 {
			t.Errorf("got exit %d when output is directory, want 2", exit)
		}
	})

	t.Run("map_is_directory", func(t *testing.T) {
		exit := Main([]string{"--bashpp", srcFile, "-o", outFile, "--map", outDir})
		if exit != 2 {
			t.Errorf("got exit %d when map is directory, want 2", exit)
		}
	})
}

func TestTranspileDashInputPath(t *testing.T) {
	dir := t.TempDir()
	dashFile := filepath.Join(dir, "-script.bpp")
	outFile := filepath.Join(dir, "out.go")

	if err := os.WriteFile(dashFile, []byte("echo hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	exitCode := Main([]string{"--bashpp", "-o", outFile, "--", dashFile})
	if exitCode != 0 {
		t.Fatalf("Main with dash input path failed with exit %d", exitCode)
	}

	if _, err := os.Stat(outFile); err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
}

func TestTranspileGoBuildAndStandaloneExecute(t *testing.T) {
	dir := t.TempDir()
	inputFile := filepath.Join(dir, "test.bpp")
	outputFile := filepath.Join(dir, "main.go")
	mapFile := filepath.Join(dir, "main.go.map")
	binFile := filepath.Join(dir, "app")
	if runtime.GOOS == "windows" {
		binFile += ".exe"
	}

	shDir, err := filepath.Abs("../../sh")
	if err != nil {
		t.Fatal(err)
	}
	goModContent := fmt.Sprintf("module testpkg\n\ngo 1.22\n\nrequire mvdan.cc/sh/v3 v3.0.0\nreplace mvdan.cc/sh/v3 => %s\n", shDir)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	script := `var x int = 42
func add(a int, b int) int {
	sum := a + b
	return sum
}
res := add(x, 10)
printf '%d\n' "$res"
`
	if err := os.WriteFile(inputFile, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	exitCode := Main([]string{"--bashpp", inputFile, "-o", outputFile})

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)
	stderr := buf.String()

	if exitCode != 0 {
		t.Fatalf("Main failed with exit %d, stderr: %s", exitCode, stderr)
	}

	outSource, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("could not read generated go output: %v", err)
	}

	// Verify exact digest matching bytes in source map artifact
	mapData, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatalf("could not read map artifact: %v", err)
	}
	var art struct {
		SchemaVersion string     `json:"schema_version"`
		Origin        string     `json:"origin"`
		GoDigest      string     `json:"go_digest"`
		Mappings      []mapEntry `json:"mappings"`
	}
	if err := json.Unmarshal(mapData, &art); err != nil {
		t.Fatalf("invalid map artifact JSON: %v", err)
	}

	if art.SchemaVersion != sourceMapSchemaVersion {
		t.Errorf("got schema_version %q, want %q", art.SchemaVersion, sourceMapSchemaVersion)
	}
	if art.Origin != inputFile {
		t.Errorf("got map origin %q, want %q", art.Origin, inputFile)
	}
	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(outSource))
	if art.GoDigest != expectedDigest {
		t.Errorf("got go_digest %q, want exact matching %q", art.GoDigest, expectedDigest)
	}
	if len(art.Mappings) == 0 {
		t.Fatal("expected non-empty mappings array in map artifact")
	}

	// Assert map meaning by Node + exact source position without assuming entry ordering
	var foundDecl1, foundFuncDecl2 bool
	for _, m := range art.Mappings {
		if m.GoLine <= 0 || m.GoCol <= 0 {
			t.Errorf("invalid go position in mapping entry: %+v", m)
		}
		if m.Node == "BashPPDecl" && m.SourceLine == 1 && m.SourceCol == 1 {
			foundDecl1 = true
		}
		if m.Node == "BashPPFuncDecl" && m.SourceLine == 2 && m.SourceCol == 1 {
			foundFuncDecl2 = true
		}
	}
	if !foundDecl1 {
		t.Error("expected mapping for Node BashPPDecl at source position 1:1 in map artifact")
	}
	if !foundFuncDecl2 {
		t.Error("expected mapping for Node BashPPFuncDecl at source position 2:1 in map artifact")
	}

	// Tidy go.mod in test directory
	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = dir
	if out, err := tidyCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v, output: %s", err, out)
	}

	// Actual go build binary
	buildCmd := exec.Command("go", "build", "-o", binFile, outputFile)
	buildCmd.Dir = dir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v, output: %s", err, out)
	}

	// Remove source script (.bpp) AND generated Go source file (.go) to prove standalone artifact execution!
	if err := os.Remove(inputFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(outputFile); err != nil {
		t.Fatal(err)
	}

	// Verify both source (.bpp) and Go (.go) are absent
	if _, err := os.Stat(inputFile); !os.IsNotExist(err) {
		t.Fatalf("expected inputFile to be absent, but stat returned: %v", err)
	}
	if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
		t.Fatalf("expected outputFile to be absent, but stat returned: %v", err)
	}

	// Create empty temp directory for PATH (containing no sh/bash binaries)
	emptyPathDir := t.TempDir()
	if _, err := os.Stat(filepath.Join(emptyPathDir, "sh")); !os.IsNotExist(err) {
		t.Fatalf("emptyPathDir should not contain sh")
	}

	// Run compiled binary with shellfree PATH pointing to empty directory
	execCmd := exec.Command(binFile)
	execCmd.Dir = dir
	execCmd.Env = []string{"PATH=" + emptyPathDir}
	runOut, err := execCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("standalone binary execution failed: %v\nOutput:\n%s", err, runOut)
	}
	if string(runOut) != "52\n" {
		t.Errorf("got program output %q, want exact %q", string(runOut), "52\n")
	}
}

func TestTranspileAtomicRollbackOnSecondRenameFailure(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.go")
	mapFile := filepath.Join(dir, "bad_map_dir") // directory path will cause rename to fail

	existingOut := []byte("// Old output content\n")
	origMode := os.FileMode(0640) // mode distinct from temp file mode 0600
	if err := os.WriteFile(outFile, existingOut, origMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outFile, origMode); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows cannot represent Unix permission bits. Still require rollback
		// to preserve the permissions the original file actually had.
		original, err := os.Stat(outFile)
		if err != nil {
			t.Fatal(err)
		}
		origMode = original.Mode().Perm()
	}

	if err := os.Mkdir(mapFile, 0755); err != nil {
		t.Fatal(err)
	}

	outData := []byte("// New Go source\n")
	mapData := []byte("{}")

	exit := writeOutputsAtomic(outFile, outData, mapFile, mapData)
	if exit != 2 {
		t.Errorf("got exit %d when map rename fails, want 2", exit)
	}

	// Verify rollback restores the original contents and permissions.
	restored, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("could not read restored output file: %v", err)
	}
	if !bytes.Equal(restored, existingOut) {
		t.Errorf("rollback failed: got %q, want %q", restored, existingOut)
	}
	st, err := os.Stat(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != origMode {
		t.Errorf("restored file mode = %o, want original %o", st.Mode().Perm(), origMode)
	}
}

func TestTranspileRepeatEmissionDeterministic(t *testing.T) {
	dir := t.TempDir()
	inputFile := filepath.Join(dir, "sample.bpp")
	outputFile1 := filepath.Join(dir, "out1.go")
	outputFile2 := filepath.Join(dir, "out2.go")

	script := `var base int = 100
echo hello
`
	if err := os.WriteFile(inputFile, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	exitCode1 := Main([]string{"--bashpp", inputFile, "-o", outputFile1})
	if exitCode1 != 0 {
		t.Fatalf("Main out1 failed: %d", exitCode1)
	}

	exitCode2 := Main([]string{"--bashpp", inputFile, "-o", outputFile2})
	if exitCode2 != 0 {
		t.Fatalf("Main out2 failed: %d", exitCode2)
	}

	src1, err := os.ReadFile(outputFile1)
	if err != nil {
		t.Fatal(err)
	}
	src2, err := os.ReadFile(outputFile2)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(src1, src2) {
		t.Errorf("emission mismatch across output paths:\nout1:\n%s\nout2:\n%s", src1, src2)
	}
}

func TestTranspileNegativeDiagnosticNoEmission(t *testing.T) {
	dir := t.TempDir()
	inputFile := filepath.Join(dir, "bad.bpp")
	outputFile := filepath.Join(dir, "out.go")
	mapFile := filepath.Join(dir, "out.go.map")

	// Pre-create output and map file to verify both are preserved untouched on error
	existingContent := []byte("// Existing content\n")
	existingMap := []byte("// Existing map\n")
	if err := os.WriteFile(outputFile, existingContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapFile, existingMap, 0644); err != nil {
		t.Fatal(err)
	}

	badScript := "var x nonexistent_type = 123\n"
	if err := os.WriteFile(inputFile, []byte(badScript), 0644); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	exitCode := Main([]string{"--bashpp", inputFile, "-o", outputFile})

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)
	stderr := buf.String()

	if exitCode != 2 {
		t.Errorf("got exit code %d, want 2", exitCode)
	}
	if !strings.Contains(stderr, "LOWER-") && !strings.Contains(stderr, "BASHPP-") {
		t.Errorf("got stderr %q, want diagnostic code", stderr)
	}

	// Verify existing output file was preserved
	currentContent, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentContent, existingContent) {
		t.Errorf("output file was modified on compile rejection: got %q, want %q", currentContent, existingContent)
	}

	// Verify existing map file was preserved
	currentMap, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentMap, existingMap) {
		t.Errorf("map file was modified on compile rejection: got %q, want %q", currentMap, existingMap)
	}

	// Verify input file content remains unchanged
	currentInput, err := os.ReadFile(inputFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(currentInput) != badScript {
		t.Errorf("input file was modified on compile rejection: got %q, want %q", currentInput, badScript)
	}

	// Subtest: diagnostic without pre-existing output or map files
	t.Run("no_preexisting_files", func(t *testing.T) {
		dir2 := t.TempDir()
		input2 := filepath.Join(dir2, "bad2.bpp")
		out2 := filepath.Join(dir2, "out2.go")
		map2 := filepath.Join(dir2, "out2.go.map")

		if err := os.WriteFile(input2, []byte(badScript), 0644); err != nil {
			t.Fatal(err)
		}

		oldStderr2 := os.Stderr
		r2, w2, _ := os.Pipe()
		os.Stderr = w2

		exitCode2 := Main([]string{"--bashpp", input2, "-o", out2})

		w2.Close()
		os.Stderr = oldStderr2

		var buf2 bytes.Buffer
		io.Copy(&buf2, r2)
		stderr2 := buf2.String()

		if exitCode2 != 2 {
			t.Errorf("got exit code %d, want 2", exitCode2)
		}
		if stderr2 == "" {
			t.Error("expected non-empty stderr on rejection")
		}

		// Verify no Go or map files were emitted
		if _, err := os.Stat(out2); !os.IsNotExist(err) {
			t.Errorf("output file was emitted on compile rejection: %v", err)
		}
		if _, err := os.Stat(map2); !os.IsNotExist(err) {
			t.Errorf("map file was emitted on compile rejection: %v", err)
		}

		// Verify input file is unchanged
		inData, err := os.ReadFile(input2)
		if err != nil {
			t.Fatal(err)
		}
		if string(inData) != badScript {
			t.Errorf("input file was modified: got %q, want %q", string(inData), badScript)
		}
	})
}

func TestFormatDiagnostic(t *testing.T) {
	pos := syntax.NewPos(12, 1, 5)

	// 1. Text field non-empty precedence: exact Text string returned
	textDiag := lower.Diagnostic{
		Code: "BASHPP-ETYPE",
		Msg:  "type mismatch",
		Pos:  pos,
		Node: "BashPPDecl",
		Text: "input.bpp: line 1: BASHPP-ETYPE: custom exact text rendering",
	}
	if got := formatDiagnostic(textDiag); got != textDiag.Text {
		t.Errorf("got formatted Text diagnostic %q, want exact Text %q", got, textDiag.Text)
	}

	// 2. BASHPP- prefix diagnostic (Text empty): exact Code + ": " + Msg without file position prefix
	bashppDiag := lower.Diagnostic{
		Code: "BASHPP-ETYPE",
		Msg:  "type mismatch",
		Pos:  pos,
		Node: "BashPPDecl",
	}
	if got := formatDiagnostic(bashppDiag); got != "BASHPP-ETYPE: type mismatch" {
		t.Errorf("got formatted BASHPP diagnostic %q, want %q", got, "BASHPP-ETYPE: type mismatch")
	}

	// 3. LOWER- prefix diagnostic (Text empty): retains file position prefix
	lowerDiag := lower.Diagnostic{
		Code: "LOWER-EUNSUPPORTED",
		Msg:  "unsupported statement",
		Pos:  pos,
		Node: "Stmt",
	}
	if got := formatDiagnostic(lowerDiag); !strings.HasPrefix(got, "1:5: LOWER-EUNSUPPORTED: unsupported statement") {
		t.Errorf("got formatted LOWER diagnostic %q, want positioned format starting with %q", got, "1:5: LOWER-EUNSUPPORTED: unsupported statement")
	}
}

func TestTranspileCLIDiagnosticsIntegration(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "unsupported_type_rejection",
			script:     "var x nonexistent_type = 123\n",
			wantExit:   2,
			wantStderr: "LOWER-",
		},
		{
			name:       "syntax_error_rejection",
			script:     "func (\n",
			wantExit:   2,
			wantStderr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			inputFile := filepath.Join(dir, "input.bpp")
			outputFile := filepath.Join(dir, "out.go")
			mapFile := filepath.Join(dir, "out.go.map")

			if err := os.WriteFile(inputFile, []byte(tt.script), 0644); err != nil {
				t.Fatal(err)
			}

			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			exitCode := Main([]string{"--bashpp", inputFile, "-o", outputFile})

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			io.Copy(&buf, r)
			stderr := buf.String()

			if exitCode != tt.wantExit {
				t.Errorf("got exit code %d, want %d", exitCode, tt.wantExit)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("got stderr %q, want it to contain %q", stderr, tt.wantStderr)
			}
			if stderr == "" {
				t.Error("expected non-empty stderr diagnostic output")
			}

			// Verify no Go output file emitted
			if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
				t.Errorf("output file was created on error: %v", err)
			}
			// Verify no source map file emitted
			if _, err := os.Stat(mapFile); !os.IsNotExist(err) {
				t.Errorf("map file was created on error: %v", err)
			}

			// Verify input file content remains unchanged
			inData, err := os.ReadFile(inputFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(inData) != tt.script {
				t.Errorf("input file content was modified: got %q, want %q", string(inData), tt.script)
			}
		})
	}
}
