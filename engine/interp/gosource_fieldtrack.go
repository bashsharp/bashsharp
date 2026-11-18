package interp

import (
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceFieldTrackForFile derives the linker's -k report for reachable,
// directly called Go-source functions and named receiver methods. The
// experiment and the linker target are independent: neither a tag nor a -k
// flag alone changes an interpreted variable.
func (r *Runner) goSourceFieldTrackForFile(file *syntax.File) (string, string) {
	if !file.GoSource || r.Env == nil {
		return "", ""
	}
	experiment := r.Env.Get("GOEXPERIMENT").String()
	for _, entry := range r.goSourceEnvironment {
		if strings.HasPrefix(entry, "GOEXPERIMENT=") {
			experiment = strings.TrimPrefix(entry, "GOEXPERIMENT=")
		}
	}
	if !goSourceFieldTrackEnabled(experiment) {
		return "", ""
	}
	flags := ""
	if fields, ok := bashPPGoFlagsFields(r.Env.Get("GOFLAGS").String()); ok {
		for _, field := range fields {
			if strings.HasPrefix(field, "-ldflags=") {
				flags = strings.TrimPrefix(field, "-ldflags=")
			}
		}
	}
	if r.bashPPTools.LinkFlags != "" {
		flags = r.bashPPTools.LinkFlags
	}
	var target string
	parts := strings.Fields(flags)
	for i := 0; i < len(parts); i++ {
		if strings.HasPrefix(parts[i], "-k=") {
			target = strings.TrimPrefix(parts[i], "-k=")
		} else if parts[i] == "-k" && i+1 < len(parts) {
			i++
			target = parts[i]
		}
	}
	if target == "" || !strings.Contains(target, ".") {
		return "", ""
	}

	type trackedType struct {
		pkg, name string
		fields    map[string]bool
	}
	types := make(map[string]trackedType)
	funcs := make(map[string][]*syntax.BashPPFuncDecl)
	for _, stmt := range file.Stmts {
		switch decl := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			if decl.Site != syntax.StartTypeDecl || len(decl.StructFields) == 0 {
				continue
			}
			pkg := "main"
			if source, ok := file.SourceAt(decl.Pos()); ok && source.PackagePath != "" {
				pkg = source.PackagePath
			}
			fields := make(map[string]bool)
			for _, field := range decl.StructFields {
				if field.Tag == nil {
					continue
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil || reflect.StructTag(tag).Get("go") != "track" {
					continue
				}
				for _, name := range field.Names {
					fields[goSourceDeclaredName(name.Value)] = true
				}
			}
			if len(fields) > 0 {
				types[decl.Name.Value] = trackedType{pkg: pkg, name: goSourceDeclaredName(decl.Name.Value), fields: fields}
			}
		case *syntax.BashPPFuncDecl:
			funcs[goSourceDeclaredName(decl.Name.Value)] = append(funcs[goSourceDeclaredName(decl.Name.Value)], decl)
		}
	}

	used := make(map[string]bool)
	seen := make(map[*syntax.BashPPFuncDecl]bool)
	var queue []*syntax.BashPPFuncDecl
	for _, decl := range funcs["main"] {
		if source, ok := file.SourceAt(decl.Pos()); ok && source.PackagePath == "" {
			queue = append(queue, decl)
		}
	}
	for len(queue) > 0 {
		decl := queue[0]
		queue = queue[1:]
		if seen[decl] || decl.Body == nil {
			continue
		}
		seen[decl] = true
		locals := make(map[string]string)
		if decl.Receiver != nil && decl.Receiver.Name != nil && decl.Receiver.RecvType != nil {
			locals[decl.Receiver.Name.Value] = decl.Receiver.RecvType.Value
		}
		syntax.Walk(decl.Body, func(node syntax.Node) bool {
			if d, ok := node.(*syntax.BashPPDecl); ok && d.Site == syntax.StartVar && d.DeclType != nil {
				locals[d.Name.Value] = d.DeclType.Value
			}
			return true
		})
		syntax.Walk(decl.Body, func(node syntax.Node) bool {
			switch x := node.(type) {
			case *syntax.BashPPCall:
				name := ""
				receiver := ""
				if len(x.Fun) > 0 {
					name = x.Fun[len(x.Fun)-1].Value
					if len(x.Fun) > 1 {
						receiver = x.Fun[0].Value
					}
				} else if sel, ok := x.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
					name = sel.Sel.Value
					if ident, ok := sel.X.(*syntax.BashPPIdent); ok {
						receiver = ident.Name.Value
					}
				}
				var owner, packageName, packagePath string
				if typ := strings.TrimPrefix(locals[receiver], "*"); typ != "" {
					if tracked, ok := types[typ]; ok {
						owner, packagePath = tracked.name, tracked.pkg
					} else if dot := strings.LastIndexByte(typ, '.'); dot >= 0 {
						packageName, owner = typ[:dot], typ[dot+1:]
					} else {
						owner = goSourceDeclaredName(typ)
					}
				}
				for _, candidate := range funcs[goSourceDeclaredName(name)] {
					if owner != "" {
						if candidate.Receiver == nil || candidate.Receiver.RecvType == nil || goSourceDeclaredName(candidate.Receiver.RecvType.Value) != owner {
							continue
						}
						candidatePackage := "main"
						if source, ok := file.SourceAt(candidate.Pos()); ok && source.PackagePath != "" {
							candidatePackage = source.PackagePath
						}
						if packagePath != "" && candidatePackage != packagePath || packagePath == "" && (packageName == "" && candidatePackage != "main" || packageName != "" && path.Base(candidatePackage) != packageName) {
							continue
						}
					}
					queue = append(queue, candidate)
				}
			case *syntax.BashPPSelectorExpr:
				ident, ok := x.X.(*syntax.BashPPIdent)
				if !ok {
					break
				}
				typeName := strings.TrimPrefix(locals[ident.Name.Value], "*")
				if tracked, ok := types[typeName]; ok && tracked.fields[goSourceDeclaredName(x.Sel.Value)] {
					used[tracked.pkg+"."+tracked.name+"."+goSourceDeclaredName(x.Sel.Value)] = true
				}
			}
			return true
		})
	}
	fields := make([]string, 0, len(used))
	for field := range used {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return target, strings.Join(fields, "\n")
}

func goSourceFieldTrackEnabled(experiment string) bool {
	enabled := false
	for _, name := range strings.Split(experiment, ",") {
		switch name {
		case "fieldtrack":
			enabled = true
		case "nofieldtrack":
			enabled = false
		}
	}
	return enabled
}

func (r *Runner) goSourceInstallFieldTrack(decl *syntax.BashPPDecl) {
	if r.goSourceFieldTrackTarget == "" || decl.Site != syntax.StartVar || decl.DeclType == nil || decl.DeclType.Value != "string" || !r.goSourceTopLevelDecl(decl) || r.exit.code != 0 {
		return
	}
	source, ok := r.bashPPGoSourceFile.SourceAt(decl.Pos())
	if !ok {
		return
	}
	pkg := source.PackagePath
	if pkg == "" && source.Package == "main" {
		pkg = "main"
	}
	if pkg+"."+goSourceDeclaredName(decl.Name.Value) != r.goSourceFieldTrackTarget {
		return
	}
	if cell := r.bashPPScope.lookup(decl.Name.Value); cell != nil {
		cell.vr = expand.Variable{Set: true, Kind: expand.String, Str: r.goSourceFieldTrackReport}
	}
}
