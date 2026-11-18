package interp

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func bashPPCgoWrapperSources(ctx context.Context, req bashPPEvalRequest) ([]string, error) {
	sources := make([]string, len(req.CgoPackages))
	for i, pkg := range req.CgoPackages {
		source, err := bashPPCgoWrapperSource(ctx, req, i, pkg)
		if err != nil {
			return nil, err
		}
		sources[i] = source
	}
	return sources, nil
}

func bashPPCgoWrapperSource(ctx context.Context, req bashPPEvalRequest, index int, pkg syntax.CgoPackage) (string, error) {
	if strings.TrimSpace(pkg.Preamble) == "" {
		return "", fmt.Errorf("gosource: cgo package %q has no preamble immediately preceding import \"C\"", pkg.Path)
	}
	var probe strings.Builder
	probe.WriteString("package main\n")
	probe.WriteString(pkg.Preamble)
	probe.WriteString("import \"C\"\nfunc __bashpp_cgo_probe(){\n")
	for _, symbol := range pkg.Symbols {
		if symbol.Kind != "func" || !syntax.BashPPValidIdent(symbol.Name) {
			return "", fmt.Errorf("gosource: cgo package %q selector C.%s is not a supported function", pkg.Path, symbol.Name)
		}
		if symbol.ResultUsed {
			probe.WriteString("_ = ")
		}
		fmt.Fprintf(&probe, "C.%s(%s)\n", symbol.Name, strings.Join(symbol.ProbeArgs, ","))
	}
	probe.WriteString("}\n")
	work, err := os.MkdirTemp("", "bashpp-cgo-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	probeFile := filepath.Join(work, "probe.go")
	objdir := filepath.Join(work, "obj")
	if err := os.Mkdir(objdir, 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(probeFile, []byte(probe.String()), 0600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, req.internalBuildGo(), "tool", "cgo", "-objdir", objdir, probeFile)
	cmd.Dir = bashPPModuleRequest(req).Dir
	cmd.Env = setEnvString(req.internalBuildEnv(), "CGO_ENABLED", "1")
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gosource: cgo unavailable for package %q: %w: %s", pkg.Path, err, strings.TrimSpace(diagnostics.String()))
	}
	generated, err := parser.ParseFile(token.NewFileSet(), filepath.Join(objdir, "_cgo_gotypes.go"), nil, 0)
	if err != nil {
		return "", fmt.Errorf("gosource: inspect cgo ABI for package %q: %w", pkg.Path, err)
	}
	types := map[string]ast.Expr{}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range generated.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			if decl.Tok == token.TYPE {
				for _, spec := range decl.Specs {
					t := spec.(*ast.TypeSpec)
					types[t.Name.Name] = t.Type
				}
			}
		case *ast.FuncDecl:
			funcs[decl.Name.Name] = decl
		}
	}
	var out strings.Builder
	out.WriteString("package main\n")
	out.WriteString(pkg.Preamble)
	out.WriteString("import \"C\"\nimport \"unsafe\"\nvar _ unsafe.Pointer\n")
	for _, symbol := range pkg.Symbols {
		decl := funcs["_Cfunc_"+symbol.Name]
		if decl == nil && symbol.Name == "malloc" {
			decl = funcs["_Cfunc__CMalloc"]
		}
		if decl == nil {
			return "", fmt.Errorf("gosource: cgo package %q function C.%s has no cmd/cgo ABI", pkg.Path, symbol.Name)
		}
		params, calls := []string{}, []string{}
		paramIndex := 0
		for _, field := range decl.Type.Params.List {
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for range count {
				name := "p" + strconv.Itoa(paramIndex)
				public, conversion, err := bashPPCgoType(field.Type, types, name, true)
				if err != nil {
					return "", fmt.Errorf("gosource: cgo package %q function C.%s parameter %d: %w", pkg.Path, symbol.Name, paramIndex, err)
				}
				params = append(params, name+" "+public)
				calls = append(calls, conversion)
				paramIndex++
			}
		}
		result := ""
		if decl.Type.Results != nil && len(decl.Type.Results.List) > 0 {
			if len(decl.Type.Results.List) != 1 {
				return "", fmt.Errorf("gosource: cgo package %q function C.%s has unsupported multiple results", pkg.Path, symbol.Name)
			}
			var conversion string
			result, conversion, err = bashPPCgoType(decl.Type.Results.List[0].Type, types, "", false)
			if err != nil {
				return "", fmt.Errorf("gosource: cgo package %q function C.%s result: %w", pkg.Path, symbol.Name, err)
			}
			if result == "void" {
				result = ""
			} else if conversion != "" {
				result += "\x00" + conversion
			}
		}
		wrapper := fmt.Sprintf("__bashpp_cgo_%d_%s", index, symbol.Name)
		resultType, resultConversion, _ := strings.Cut(result, "\x00")
		fmt.Fprintf(&out, "func %s(%s)", wrapper, strings.Join(params, ","))
		if resultType != "" {
			out.WriteString(" " + resultType)
		}
		out.WriteString("{")
		call := fmt.Sprintf("C.%s(%s)", symbol.Name, strings.Join(calls, ","))
		if resultType == "" {
			out.WriteString(call)
		} else if resultConversion == "" {
			out.WriteString("return " + call)
		} else {
			out.WriteString("return " + strings.ReplaceAll(resultConversion, "$", call))
		}
		out.WriteString("}\n")
	}
	formatted, err := format.Source([]byte(out.String()))
	if err != nil {
		return "", fmt.Errorf("gosource: generate cgo wrapper for package %q: %w", pkg.Path, err)
	}
	return string(formatted), nil
}

// bashPPCgoType maps cmd/cgo's private ABI spelling to a transportable public
// worker signature. C aggregate values are deliberately refused; pointers to
// them cross only as opaque unsafe.Pointer handles.
func bashPPCgoType(expr ast.Expr, defs map[string]ast.Expr, value string, input bool) (string, string, error) {
	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		if id, ok := expr.X.(*ast.Ident); ok && id.Name == "unsafe" && expr.Sel.Name == "Pointer" {
			return "unsafe.Pointer", value, nil
		}
	case *ast.StarExpr:
		if id, ok := expr.X.(*ast.Ident); ok && strings.HasPrefix(id.Name, "_Ctype_") {
			name := strings.TrimPrefix(id.Name, "_Ctype_")
			if input {
				return "unsafe.Pointer", "(*C." + name + ")(" + value + ")", nil
			}
			return "unsafe.Pointer", "unsafe.Pointer($)", nil
		}
	case *ast.Ident:
		if expr.Name == "_Ctype_void" {
			return "void", "", nil
		}
		if strings.HasPrefix(expr.Name, "_Ctype_") {
			underlying := defs[expr.Name]
			for {
				id, ok := underlying.(*ast.Ident)
				if !ok || !strings.HasPrefix(id.Name, "_Ctype_") {
					break
				}
				underlying = defs[id.Name]
			}
			id, ok := underlying.(*ast.Ident)
			if !ok || !isCgoScalarType(id.Name) {
				return "", "", fmt.Errorf("unsupported C value type %s", expr.Name)
			}
			cname := strings.TrimPrefix(expr.Name, "_Ctype_")
			if input {
				return id.Name, "C." + cname + "(" + value + ")", nil
			}
			return id.Name, id.Name + "($)", nil
		}
		if isCgoScalarType(expr.Name) {
			return expr.Name, value, nil
		}
	}
	var text bytes.Buffer
	_ = format.Node(&text, token.NewFileSet(), expr)
	return "", "", fmt.Errorf("unsupported C ABI type %s", text.String())
}

func isCgoScalarType(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "float32", "float64":
		return true
	}
	return false
}
