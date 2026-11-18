package audit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/scanner"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// ReadRoots reads a roots list (one corpus-relative path per line, '#' comments).
func ReadRoots(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var roots []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		roots = append(roots, line)
	}
	return roots, sc.Err()
}

// LoadRoot resolves a root under corpus into the source that upstream would
// compile. `// errorcheck` roots are compiled as-is. `// errorcheckoutput
// prog.go` roots are first run (`go run root prog.go`) and their stdout is the
// program to check, named tmp__.go as upstream does; that needs a `go` on
// PATH and is the only subprocess this audit ever starts.
func LoadRoot(corpus, root string) (RootSpec, error) {
	full := filepath.Join(corpus, root)
	src, err := os.ReadFile(full)
	if err != nil {
		return RootSpec{}, err
	}
	header, _, _ := bytes.Cut(src, []byte("\n"))
	spec := RootSpec{Root: root, Header: strings.TrimSpace(string(header)), Short: filepath.Base(root), Src: src}
	fields := strings.Fields(spec.Header)
	if len(fields) >= 2 && fields[1] == "errorcheckoutput" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		args := []string{"run", full}
		for _, a := range fields[2:] {
			args = append(args, filepath.Join(filepath.Dir(full), a))
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = filepath.Dir(full)
		out, err := cmd.Output()
		if err != nil {
			return spec, fmt.Errorf("%s: go %s: %v", root, strings.Join(args, " "), err)
		}
		spec.Short, spec.Src = "tmp__.go", out
	}
	return spec, nil
}

// FrontEnd runs the Bash++ front end (gosource.Load with default Options —
// the same path `--check` takes) over one root and returns its diagnostics
// in emission order, each tagged with the stage that produced it.
func FrontEnd(spec RootSpec) (diags []Diag, relatedFolded int) {
	_, err := gosource.Load([]gosource.Source{{Name: spec.Short, Data: spec.Src}}, gosource.Options{})
	if err == nil {
		return nil, 0
	}
	list, ok := err.(gosource.ErrorList)
	if !ok {
		list = gosource.ErrorList{err}
	}
	for _, e := range list {
		stage := StageChecker
		// go/types reports the later parts of a multi-part error as separate
		// errors whose Msg starts with "\t"; gc prints them as "\t"-prefixed
		// continuation lines of the main diagnostic. Fold them the same way
		// so the audit measures the diagnostic, not the printer.
		if te, ok := e.(types.Error); ok && strings.HasPrefix(te.Msg, "\t") && len(diags) > 0 {
			pos := strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(te.Error(), te.Msg)), ":")
			diags[len(diags)-1].Raw += "\n\t" + pos + ": " + strings.TrimPrefix(te.Msg, "\t")
			relatedFolded++
			continue
		}
		switch e := e.(type) {
		case *scanner.Error:
			stage = StageParser
			if IsScannerMessage(e.Msg) {
				stage = StageScanner
			}
		case scanner.Error:
			stage = StageParser
			if IsScannerMessage(e.Msg) {
				stage = StageScanner
			}
		case types.Error:
			stage = StageChecker
		}
		for _, raw := range SplitOutput(e.Error()) {
			diags = append(diags, Diag{Raw: raw, Line: LineOf(raw), Stage: stage})
		}
	}
	return diags, relatedFolded
}

// Audit classifies one root against the Bash++ front end.
func Audit(spec RootSpec) (Result, error) {
	wants, err := WantedErrors(spec.Src, spec.Short)
	if err != nil {
		return Result{}, err
	}
	diags, folded := FrontEnd(spec)
	r := Classify(spec.Root, spec.Header, wants, diags)
	r.RelatedFolded = folded
	return r, nil
}
