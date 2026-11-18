// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const bashPPMappedCompanionListTemplate = "{{.ImportPath}}\n{{range .GoFiles}}go:{{.}}\n{{end}}{{range .SFiles}}s:{{.}}\n{{end}}\n--bashpp-mapped-companion--\n"

func bashPPMappedCompanionSource(pkg bashPPMappedCompanion) (string, error) {
	var source strings.Builder
	fmt.Fprintf(&source, "package %s\n\n", pkg.Name)
	for _, fn := range pkg.Funcs {
		if !syntax.BashPPValidIdent(fn.Name) {
			return "", fmt.Errorf("gosource: invalid mapped companion function %q", fn.Name)
		}
		if fn.Linkname != "" {
			return "", fmt.Errorf("gosource: mapped companion function %s has unsupported linkname", fn.Name)
		}
		if fn.Results == "" {
			fmt.Fprintf(&source, "func %s(%s)\n", fn.Name, fn.Params)
		} else {
			fmt.Fprintf(&source, "func %s(%s)(%s)\n", fn.Name, fn.Params, fn.Results)
		}
	}
	fmt.Fprintf(&source, "func %s() map[string]any { return map[string]any{\n", pkg.Hook)
	for _, fn := range pkg.Funcs {
		fmt.Fprintf(&source, "%q: %s,\n", fn.Name, fn.Name)
	}
	source.WriteString("} }\n")
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return "", fmt.Errorf("gosource: mapped companion package %q helper: %w", pkg.Path, err)
	}
	return string(formatted), nil
}

func bashPPOverlayMappedCompanions(ctx context.Context, req bashPPEvalRequest, file *bashPPImportSource, env []string) error {
	var checks []bashPPMappedCompanionCheck
	for i, pkg := range req.MappedCompanions {
		stubs, err := bashPPCompanionRootStubs(pkg.SourceDir, pkg.SourceFiles)
		if err != nil {
			return fmt.Errorf("gosource: mapped package %q: %w", pkg.Path, err)
		}
		if err := file.overlayPackage(pkg.SourceDir, stubs); err != nil {
			return err
		}
		source, err := bashPPMappedCompanionSource(pkg)
		if err != nil {
			return err
		}
		var roots []string
		for path := range stubs {
			if bashPPCompanionGoFileName(filepath.Base(path)) && bashPPCompanionFileBuilt(path) {
				roots = append(roots, path)
			}
		}
		sort.Strings(roots)
		if len(roots) == 0 {
			return fmt.Errorf("gosource: mapped companion package %q has no selected Go file to overlay", pkg.Path)
		}
		name := filepath.Base(roots[0])
		if err := file.replacePackageSource(roots[0], fmt.Sprintf("bashpp-companion-%d.go", i), source); err != nil {
			return err
		}
		checks = append(checks, bashPPMappedCompanionCheck{pkg: pkg, generated: name})
	}
	return bashPPMappedCompanionBuildIsolated(ctx, req, file, env, checks)
}

type bashPPMappedCompanionCheck struct {
	pkg       bashPPMappedCompanion
	generated string
}

func bashPPMappedCompanionBuildIsolated(ctx context.Context, req bashPPEvalRequest, file *bashPPImportSource, env []string, checks []bashPPMappedCompanionCheck) error {
	if len(checks) == 0 {
		return nil
	}
	args := []string{"list", "-overlay=" + file.overlay, "-f", bashPPMappedCompanionListTemplate}
	for _, check := range checks {
		args = append(args, check.pkg.Path)
	}
	list := exec.CommandContext(ctx, req.internalBuildGo(), args...)
	list.Dir, list.Env = file.sourceDir, setEnvString(env, "PWD", file.sourceDir)
	var out, diagnostics bytes.Buffer
	list.Stdout, list.Stderr = &out, &diagnostics
	if err := list.Run(); err != nil {
		return fmt.Errorf("gosource: list mapped companion packages: %w: %s", err, diagnostics.String())
	}
	records := bashPPMappedCompanionListRecords(out.String())
	if len(records) != len(checks) {
		return fmt.Errorf("gosource: listed %d mapped companion packages, want %d", len(records), len(checks))
	}
	byPath := make(map[string][]string, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		path := record[0]
		if _, ok := byPath[path]; ok {
			return fmt.Errorf("gosource: listed mapped companion package %q more than once", path)
		}
		byPath[path] = record[1:]
	}
	for _, check := range checks {
		record, ok := byPath[check.pkg.Path]
		if !ok {
			return fmt.Errorf("gosource: mapped companion package %q was not listed", check.pkg.Path)
		}
		if err := bashPPValidateMappedCompanionSelection(file, check.pkg, check.generated, record); err != nil {
			return err
		}
	}
	return nil
}

func bashPPMappedCompanionListRecords(output string) [][]string {
	var records [][]string
	for _, record := range strings.Split(output, "\n--bashpp-mapped-companion--\n") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		records = append(records, strings.Fields(record))
	}
	return records
}

func bashPPValidateMappedCompanionSelection(file *bashPPImportSource, pkg bashPPMappedCompanion, generated string, listed []string) error {
	selected := make(map[string]bool, len(pkg.Files))
	canonicalDir, err := filepath.Abs(pkg.SourceDir)
	if err != nil {
		return err
	}
	canonicalDir, err = filepath.EvalSymlinks(canonicalDir)
	if err != nil {
		return err
	}
	for _, path := range pkg.Files {
		if !filepath.IsAbs(path) {
			path = filepath.Join(canonicalDir, path)
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("gosource: mapped companion package %q file %s: %w", pkg.Path, path, err)
		}
		if parent != canonicalDir || !strings.EqualFold(filepath.Ext(path), ".s") {
			return fmt.Errorf("gosource: mapped companion package %q file %s is not assembly in its source directory", pkg.Path, path)
		}
		name := filepath.Base(path)
		if selected[name] {
			return fmt.Errorf("gosource: mapped companion package %q selects assembly file %s more than once", pkg.Path, name)
		}
		selected[name] = true
	}
	var got []string
	generatedSeen := false
	for _, line := range listed {
		kind, name, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch kind {
		case "go":
			if name == generated {
				generatedSeen = true
				continue
			}
			if _, ok := file.replace[filepath.Join(canonicalDir, name)]; !ok {
				return fmt.Errorf("gosource: mapped companion package %q would compile original file %s", pkg.Path, name)
			}
		case "s":
			got = append(got, name)
			if !selected[name] {
				return fmt.Errorf("gosource: mapped companion package %q selected undeclared assembly file %s", pkg.Path, name)
			}
		}
	}
	if !generatedSeen {
		return fmt.Errorf("gosource: mapped companion package %q does not include its generated helper (selected %q)", pkg.Path, strings.Join(listed, " "))
	}
	sort.Strings(got)
	var want []string
	for name := range selected {
		if bashPPCompanionFileBuilt(filepath.Join(pkg.SourceDir, name)) {
			want = append(want, name)
		}
	}
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		return fmt.Errorf("gosource: mapped companion package %q built assembly files %v, want exact selection %v", pkg.Path, got, want)
	}
	return nil
}
