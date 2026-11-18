// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPEmbedDecl is the declaration-only part of a Go embed directive. The
// native dependency helper compiles the directive, while original function
// bodies remain interpreted.
type bashPPEmbedDecl struct {
	Name       string
	Type       string
	Directives []string
}

type bashPPNativeFuncDecl struct {
	Name string
	// RuntimeName is the flattened interpreter binding. It is empty when the
	// declaration belongs to the program package and Name is already exact.
	RuntimeName string
	Params      string
	Results     string
	// Linkname is the linker target of a two-argument //go:linkname on this
	// bodyless declaration. The dependency helper must repeat the directive:
	// a bare declaration creates a reference to a local object which does not
	// exist, rather than the alias the original package declared.
	Linkname string
}

type bashPPMappedCompanion struct {
	Path, Name, SourceDir, Hook string
	SourceFiles, Files          []string
	Funcs                       []bashPPNativeFuncDecl
}

const bashPPEmbedSymbolPrefix = "\x00gosource.embed."

func bashPPGoSourceEmbedDecls(file *syntax.File) []bashPPEmbedDecl {
	if file == nil || !file.GoSource {
		return nil
	}
	var decls []bashPPEmbedDecl
	for _, stmt := range file.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPDecl)
		if !ok || decl.Site != syntax.StartVar || decl.Name == nil || decl.DeclTypeExpr == nil {
			continue
		}
		var directives []string
		for _, comment := range stmt.Comments {
			if strings.HasPrefix(comment.Text, "go:embed ") || strings.HasPrefix(comment.Text, "go:embed\t") {
				directives = append(directives, comment.Text)
			}
		}
		if len(directives) > 0 {
			decls = append(decls, bashPPEmbedDecl{
				Name: decl.Name.Value, Type: bashPPBridgeTypeText(decl.DeclTypeExpr), Directives: directives,
			})
		}
	}
	return decls
}

func (r *Runner) bashPPGoSourceEmbedRequest() ([]bashPPEmbedDecl, string) {
	decls := bashPPGoSourceEmbedDecls(r.bashPPGoSourceFile)
	if len(decls) == 0 {
		return nil, ""
	}
	return decls, r.bashPPGoSourceSourceDir()
}

func (r *Runner) bashPPGoSourceSourceDir() string {
	sourceFile := r.bashPPGoSourceSourceFile()
	if sourceFile == "" {
		return r.Dir
	}
	return filepath.Dir(sourceFile)
}

func (r *Runner) bashPPGoSourceSourceFile() string {
	if r.bashPPGoSourceFile == nil {
		return ""
	}
	name := r.bashPPGoSourceFile.Name
	if name == "" {
		return ""
	}
	if !filepath.IsAbs(name) {
		base := r.Dir
		if r.bashPPTools.moduleDir != "" {
			base = r.bashPPTools.moduleDir
		}
		name = filepath.Join(base, name)
	}
	return name
}

// bashPPGoSourceRootFiles lists the original inputs of the interpreted
// program's own package, as the loader recorded them. A linked package carries
// a declared import path; the program package is the one that does not.
func (r *Runner) bashPPGoSourceRootFiles() []string {
	if r.bashPPGoSourceFile == nil {
		return nil
	}
	var out []string
	for _, source := range r.bashPPGoSourceFile.Sources {
		if source.PackagePath != "" || source.Name == "" {
			continue
		}
		out = append(out, source.Name)
	}
	sort.Strings(out)
	return out
}

// bashPPGoSourceNativeCompanions reports the same-package object companions
// of the interpreted Go root, the no-body declarations they may satisfy, the
// original functions they call back into, and the companion frames the runtime
// has no pointer map for. An unsound reference — package data the interpreter
// owns — is refused here rather than linked.
func (r *Runner) bashPPGoSourceNativeCompanions(sourceDir string) ([]string, []bashPPNativeFuncDecl, []bashPPCompanionTrampoline, []string, error) {
	if !r.bashPPGoSource || r.bashPPGoSourceFile == nil || sourceDir == "" {
		return nil, nil, nil, nil, nil
	}
	var funcs []bashPPNativeFuncDecl
	for _, stmt := range r.bashPPGoSourceFile.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok || decl.Body != nil || decl.Receiver != nil || decl.Name == nil || len(decl.TypeParams) > 0 {
			continue
		}
		if source, ok := r.bashPPGoSourceFile.SourceAt(decl.Pos()); ok && source.PackagePath != "" {
			continue
		}
		linkname := ""
		for _, comment := range stmt.Comments {
			fields := strings.Fields(comment.Text)
			if len(fields) == 3 && fields[0] == "go:linkname" && fields[1] == goSourceDeclaredName(decl.Name.Value) {
				linkname = fields[2]
				break
			}
		}
		if linkname == "" {
			// The directive may precede the declaration without being
			// attached to it (it follows a doc comment); it names the
			// declaration within the same source file.
			if _, target, ok, _ := r.goSourceLinknameIn(decl.Pos(), decl.Name.Value); ok {
				linkname = target
			}
		}
		if r.goSourceLinknameTargetInterpreted(linkname) {
			continue
		}
		funcs = append(funcs, bashPPNativeFuncDecl{
			Name:     decl.Name.Value,
			Params:   bashPPBridgeFieldsTextIn(decl.Params, r.bashPPScopedLocalTypeName),
			Results:  bashPPBridgeFieldsTextIn(decl.Results, r.bashPPScopedLocalTypeName),
			Linkname: linkname,
		})
	}
	if len(funcs) == 0 {
		return nil, nil, nil, nil, nil
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, funcs, nil, nil, nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.EqualFold(filepath.Ext(name), ".s") {
			files = append(files, filepath.Join(sourceDir, name))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, funcs, nil, nil, nil
	}
	trampolines, unmapped, err := r.bashPPGoSourceCompanionTrampolines(files)
	if err != nil {
		return files, funcs, nil, nil, err
	}
	return files, funcs, trampolines, unmapped, nil
}

// bashPPGoSourceMappedCompanions turns the loader's authenticated package
// metadata into build declarations. Companion paths are never discovered
// here: the caller selected them before loading the package map.
func (r *Runner) bashPPGoSourceMappedCompanions() ([]bashPPMappedCompanion, error) {
	if !r.bashPPGoSource || r.bashPPGoSourceFile == nil {
		return nil, nil
	}
	var out []bashPPMappedCompanion
	for _, meta := range r.bashPPGoSourceFile.GoPackageCompanions {
		if meta.Path == "" || meta.Name == "" || meta.SourceDir == "" || meta.Hook == "" || len(meta.Files) == 0 {
			return nil, fmt.Errorf("gosource: mapped package %q companion metadata is incomplete", meta.Path)
		}
		mapped := bashPPMappedCompanion{
			Path: meta.Path, Name: meta.Name, SourceDir: meta.SourceDir, Hook: meta.Hook,
			SourceFiles: append([]string(nil), meta.SourceFiles...), Files: append([]string(nil), meta.Files...),
		}
		symbols := make(map[string]string, len(meta.Symbols))
		for _, symbol := range meta.Symbols {
			symbols[symbol.RuntimeName] = symbol.Name
		}
		for _, stmt := range r.bashPPGoSourceFile.Stmts {
			decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
			if !ok || decl.Body != nil || decl.Receiver != nil || decl.Name == nil || len(decl.TypeParams) > 0 {
				continue
			}
			original := symbols[decl.Name.Value]
			if original == "" {
				continue
			}
			mapped.Funcs = append(mapped.Funcs, bashPPNativeFuncDecl{
				Name: original, RuntimeName: decl.Name.Value,
				Params:  bashPPBridgeFieldsTextIn(decl.Params, r.bashPPScopedLocalTypeName),
				Results: bashPPBridgeFieldsTextIn(decl.Results, r.bashPPScopedLocalTypeName),
			})
		}
		if len(mapped.Funcs) == 0 {
			return nil, fmt.Errorf("gosource: mapped package %q companions select no body-less declarations", meta.Path)
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (r *Runner) bashPPNativeEmbedDeclaration(d *syntax.BashPPDecl) bool {
	if !r.bashPPGoSource || d == nil || d.Site != syntax.StartVar || d.Name == nil {
		return false
	}
	found := false
	for _, embed := range bashPPGoSourceEmbedDecls(r.bashPPGoSourceFile) {
		if embed.Name == d.Name.Value {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	req, err := r.bashPPEvalRequest()
	if err == nil {
		var values []bashPPBridgeValue
		values, err = r.bashPPNativeRequest(r.ectx, req, bashPPBridgeRequest{Op: "get", Selector: bashPPEmbedSymbolPrefix + d.Name.Value})
		if err == nil {
			if len(values) != 1 {
				err = fmt.Errorf("gosource: embedded declaration returned %d values", len(values))
			} else if d.Name.Value != "_" {
				r.bashPPBindNativeValue(d.Name.Value, values[0])
			}
		}
	}
	if err != nil {
		r.exit.fatal(&goSourceError{prefix: r.bashErrPrefix(d.Pos()), err: err})
	}
	return true
}

// goSourceLinknameTargetInterpreted reports a //go:linkname target symbol whose
// package is one of the packages this program interprets.
func (r *Runner) goSourceLinknameTargetInterpreted(target string) bool {
	if target == "" || r.bashPPGoSourceFile == nil {
		return false
	}
	slash := strings.LastIndex(target, "/")
	dot := strings.Index(target[slash+1:], ".")
	if dot < 0 {
		return false
	}
	pkg := target[:slash+1+dot]
	for _, source := range r.bashPPGoSourceFile.Sources {
		if source.PackagePath != "" && source.PackagePath == pkg {
			return true
		}
	}
	return false
}
