// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// An assembly companion is compiled as itself, but it lives in the same
// package as an interpreted Go root. It may therefore name package-level
// symbols the Go side owns. Only one of those can be honoured soundly: a
// function, whose body stays interpreted behind a generated trampoline. A
// package variable is interpreter-owned storage and is refused by name.

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPCompanionTrampoline is a package-level original function an assembly
// companion references. The helper declares it so the companion links; the
// generated body is protocol only and the original body stays interpreted.
type bashPPCompanionTrampoline struct {
	Name    string
	Params  []string
	Results []string
}

// bashPPCompanionSelectorPrefix keys a trampoline callback. The prefix cannot
// collide with a mirrored method selector, which is always "Type.Method".
const bashPPCompanionSelectorPrefix = "\x00gosource.companion."

const bashPPAsmMiddleDot = "·"

// bashPPAsmIdentByte reports a byte that may appear in an assembly symbol name.
func bashPPAsmIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// bashPPAsmQualifierByte additionally accepts the package-path characters that
// may precede the middle dot, so an import-path-qualified reference is never
// mistaken for a reference to this package.
func bashPPAsmQualifierByte(b byte) bool {
	return bashPPAsmIdentByte(b) || b == '.' || b == '/' || b == '-'
}

// bashPPAsmLineSymbols lists, in source order, the package-level symbols of
// the current package that one assembly line names. A file-static symbol
// (`·name<>(SB)`) and a symbol qualified by another package are both skipped:
// neither can resolve to the original Go root.
func bashPPAsmLineSymbols(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		j := strings.Index(line[i:], bashPPAsmMiddleDot)
		if j < 0 {
			break
		}
		at := i + j
		prefix := line[:at]
		k := len(prefix)
		for k > 0 && bashPPAsmQualifierByte(prefix[k-1]) {
			k--
		}
		qualifier := prefix[k:]
		rest := line[at+len(bashPPAsmMiddleDot):]
		n := 0
		for n < len(rest) && bashPPAsmIdentByte(rest[n]) {
			n++
		}
		name, tail := rest[:n], rest[n:]
		i = at + len(bashPPAsmMiddleDot) + n
		if name == "" || (qualifier != "" && qualifier != "main") || strings.HasPrefix(tail, "<>") {
			continue
		}
		// A static base offset is part of the reference, not of the name.
		for len(tail) > 1 && (tail[0] == '+' || tail[0] == '-') {
			m := 1
			for m < len(tail) && tail[m] >= '0' && tail[m] <= '9' {
				m++
			}
			if m == 1 {
				break
			}
			tail = tail[m:]
		}
		if !strings.HasPrefix(tail, "(SB)") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// bashPPAsmSymbols accumulates the symbols one assembly file defines itself
// and the ones it expects the Go side of the package to provide.
func bashPPAsmSymbols(text string, defined, referenced map[string]bool) {
	text = bashPPAsmStripBlockComments(text)
	for _, line := range strings.Split(text, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		defines := false
		switch fields[0] {
		case "TEXT", "GLOBL", "DATA":
			defines = true
		}
		for at, name := range bashPPAsmLineSymbols(line) {
			if defines && at == 0 {
				defined[name] = true
				continue
			}
			referenced[name] = true
		}
	}
}

// bashPPAsmStripBlockComments removes /* */ comments so a commented-out symbol
// reference never asks for a trampoline.
func bashPPAsmStripBlockComments(text string) string {
	var b strings.Builder
	for {
		open := strings.Index(text, "/*")
		if open < 0 {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:open])
		rest := text[open+2:]
		close := strings.Index(rest, "*/")
		if close < 0 {
			return b.String()
		}
		// Keep the line structure: a block comment may span lines.
		b.WriteString(strings.Repeat("\n", strings.Count(rest[:close], "\n")))
		text = rest[close+2:]
	}
}

// bashPPCompanionFileBuilt reports whether the toolchain building the helper
// would compile this companion for the host. Only those files decide which
// original symbols the link actually needs.
func bashPPCompanionFileBuilt(path string) bool {
	matched, err := build.Default.MatchFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		// An unreadable constraint is analysed rather than skipped: a missed
		// reference would only be reported later as a link failure, but a
		// missed refusal must never become a silent pass.
		return true
	}
	return matched
}

// bashPPGoSourceCompanionTrampolines classifies the package-level symbols the
// built assembly companions expect the Go root to define. A function with an
// interpreted body becomes a trampoline; a package variable is refused. It also
// reports the companion frames that carry no locals pointer map, which the
// helper must keep still rather than refuse.
func (r *Runner) bashPPGoSourceCompanionTrampolines(files []string) ([]bashPPCompanionTrampoline, []string, error) {
	if len(files) == 0 || r.bashPPGoSourceFile == nil {
		return nil, nil, nil
	}
	defined, referenced := map[string]bool{}, map[string]bool{}
	frames := map[string]bool{}
	for _, file := range files {
		if !bashPPCompanionFileBuilt(file) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, fmt.Errorf("gosource: assembly companion %s: %w", filepath.Base(file), err)
		}
		bashPPAsmSymbols(string(data), defined, referenced)
		bashPPAsmUnmappedFrames(string(data), frames)
	}
	unmapped := make([]string, 0, len(frames))
	for name := range frames {
		unmapped = append(unmapped, name)
	}
	sort.Strings(unmapped)
	names := make([]string, 0, len(referenced))
	for name := range referenced {
		if !defined[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []bashPPCompanionTrampoline
	for _, name := range names {
		for _, stmt := range r.bashPPGoSourceFile.Stmts {
			switch decl := stmt.Cmd.(type) {
			case *syntax.BashPPFuncDecl:
				if decl.Receiver != nil || decl.Name == nil || decl.Name.Value != name || decl.Body == nil {
					continue
				}
				trampoline, err := r.bashPPCompanionTrampoline(decl)
				if err != nil {
					return nil, nil, err
				}
				out = append(out, trampoline)
			case *syntax.BashPPDecl:
				if decl.Site != syntax.StartVar || decl.Name == nil || decl.Name.Value != name {
					continue
				}
				return nil, nil, fmt.Errorf("gosource: assembly companion references original variable %s: interpreted package state cannot be shared with compiled code", name)
			default:
				continue
			}
			break
		}
	}
	return out, unmapped, nil
}

// bashPPCompanionTrampoline derives the trampoline signature from the original
// declaration. Anything the transport cannot spell is refused by name rather
// than widened into an untyped call.
func (r *Runner) bashPPCompanionTrampoline(decl *syntax.BashPPFuncDecl) (bashPPCompanionTrampoline, error) {
	name := decl.Name.Value
	if name == "main" || name == "init" {
		return bashPPCompanionTrampoline{}, fmt.Errorf("gosource: assembly companion references original %s, which the dependency helper defines itself", name)
	}
	if len(decl.TypeParams) > 0 {
		return bashPPCompanionTrampoline{}, fmt.Errorf("gosource: assembly companion references original generic function %s", name)
	}
	params, ok := r.bashPPCompanionFieldTypes(decl.Params)
	if !ok {
		return bashPPCompanionTrampoline{}, fmt.Errorf("gosource: assembly companion references original function %s, whose parameters the callback transport cannot spell", name)
	}
	results, ok := r.bashPPCompanionFieldTypes(decl.Results)
	if !ok {
		return bashPPCompanionTrampoline{}, fmt.Errorf("gosource: assembly companion references original function %s, whose results the callback transport cannot spell", name)
	}
	return bashPPCompanionTrampoline{Name: name, Params: params, Results: results}, nil
}

// bashPPCompanionFieldTypes renders a parameter or result list one spelling per
// declared value, in the original program's own names.
func (r *Runner) bashPPCompanionFieldTypes(fields []*syntax.BashPPField) ([]string, bool) {
	var out []string
	for _, field := range fields {
		if field.Variadic() || field.FieldTypeExpr == nil {
			return nil, false
		}
		text := bashPPBridgeTypeTextIn(field.FieldTypeExpr, r.bashPPScopedLocalTypeName)
		if text == "" || strings.Contains(text, "<inferred>") {
			return nil, false
		}
		for range max(len(field.Names), 1) {
			out = append(out, text)
		}
	}
	return out, true
}

// bashPPCompanionTrampolineGo emits one trampoline. The whole body is
// generated protocol: arguments are encoded onto a callback, the interpreter
// runs the original body, and each result is decoded at its declared type.
// The protocol runs off this frame, because the companion frame underneath it
// may be one the runtime has no map for; see companionOffFrame in the helper.
func bashPPCompanionTrampolineGo(name string, params, results []string) string {
	var b strings.Builder
	decls := make([]string, len(params))
	encoded := make([]string, len(params))
	for i, typ := range params {
		decls[i] = fmt.Sprintf("bpparg%d %s", i, typ)
		encoded[i] = fmt.Sprintf("encode(reflect.ValueOf(bpparg%d))", i)
	}
	returns := make([]string, len(results))
	for i, typ := range results {
		returns[i] = fmt.Sprintf("bppres%d %s", i, typ)
	}
	out := strings.Join(returns, ", ")
	if out != "" {
		out = " (" + out + ")"
	}
	fmt.Fprintf(&b, "func %s(%s)%s {\n", name, strings.Join(decls, ", "), out)
	b.WriteString(" companionOffFrame(func(){\n")
	fmt.Fprintf(&b, " recv:=value{Kind:\"companion\",Text:%q}\n", name)
	if len(encoded) > 0 {
		fmt.Fprintf(&b, " recv.CallArgs=[]value{%s}\n", strings.Join(encoded, ","))
	}
	fmt.Fprintf(&b, " out,err:=callback(%q,recv);if err!=nil{panic(err)}\n", bashPPCompanionSelectorPrefix+name)
	fmt.Fprintf(&b, " if len(out)!=%d{panic(fmt.Errorf(%q))}\n", len(results),
		fmt.Sprintf("original %s result count mismatch", name))
	for i, typ := range results {
		fmt.Fprintf(&b, " bppval%d,err:=decode(out[%d],reflect.TypeFor[%s]());if err!=nil{panic(err)}\n", i, i, typ)
		fmt.Fprintf(&b, " if bppval%d.IsValid(){bppres%d,_=bppval%d.Interface().(%s)}\n", i, i, i, typ)
	}
	b.WriteString(" })\n return\n}\n")
	return b.String()
}

// A framed assembly companion is the second thing that lives in the same
// package and is not the interpreter's to arrange. `TEXT ·f(SB),NOSPLIT,$8`
// with no NO_LOCAL_POINTERS declares locals and no map for them. Nothing ever
// asks that frame for its map natively, because the only thing such a frame
// calls is a leaf that neither grows a stack nor reaches a collector safe
// point. What it calls here is a trampoline into the interpreter, which is
// both. The helper answers that by holding a stack move and a stack scan off
// for the whole call; these report the frames that make that necessary.

// bashPPAsmTextName reports the symbol a TEXT line defines, including a
// file-static one: a static frame is still a frame the goroutine carries.
func bashPPAsmTextName(line string) string {
	at := strings.Index(line, bashPPAsmMiddleDot)
	if at < 0 {
		return ""
	}
	rest := line[at+len(bashPPAsmMiddleDot):]
	n := 0
	for n < len(rest) && bashPPAsmIdentByte(rest[n]) {
		n++
	}
	return rest[:n]
}

// bashPPAsmTextFramed reports whether a TEXT line declares local frame bytes.
// Its last operand is `$frame-args`; a frame size spelled as anything but a
// literal is reported as framed, since a missed frame is a fatal runtime error
// rather than a refusal.
func bashPPAsmTextFramed(line string) bool {
	at := strings.LastIndex(line, "$")
	if at < 0 {
		return false
	}
	spec := strings.TrimSpace(line[at+1:])
	i := 0
	if i < len(spec) && (spec[i] == '-' || spec[i] == '+') {
		i++
	}
	j := i
	for j < len(spec) && spec[j] >= '0' && spec[j] <= '9' {
		j++
	}
	if j == i {
		return true
	}
	size, err := strconv.Atoi(spec[:j])
	if err != nil {
		return true
	}
	return size > 0
}

// bashPPAsmLocalsMapped reports a line that gives the enclosing TEXT a locals
// pointer map: NO_LOCAL_POINTERS, or FUNCDATA at the locals index itself.
func bashPPAsmLocalsMapped(fields []string, line string) bool {
	switch fields[0] {
	case "NO_LOCAL_POINTERS":
		return true
	case "FUNCDATA":
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "FUNCDATA"))
		return strings.HasPrefix(rest, "$1,") || strings.HasPrefix(rest, "$1 ") ||
			strings.Contains(rest, "FUNCDATA_LocalsPointerMaps")
	}
	return false
}

// bashPPAsmUnmappedFrames accumulates the symbols one assembly file defines
// with a local frame and no map for it.
func bashPPAsmUnmappedFrames(text string, unmapped map[string]bool) {
	text = bashPPAsmStripBlockComments(text)
	name, framed, mapped := "", false, false
	finish := func() {
		if name != "" && framed && !mapped {
			unmapped[name] = true
		}
		name, framed, mapped = "", false, false
	}
	for _, line := range strings.Split(text, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "TEXT":
			finish()
			name, framed = bashPPAsmTextName(line), bashPPAsmTextFramed(line)
			continue
		case "GLOBL", "DATA":
			finish()
			continue
		}
		if name != "" && bashPPAsmLocalsMapped(fields, line) {
			mapped = true
		}
	}
	finish()
}
