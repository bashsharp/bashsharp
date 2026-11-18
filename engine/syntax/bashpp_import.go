package syntax

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/mod/module"
)

//go:generate go run gen_go127stdlib.go

// bashppImport runs only after an ordinary CallExpr has reached its
// terminator. Refusing a shape returns the original tree untouched.
func bashppImport(ce *CallExpr, redirs []*Redirect) *BashPPImport {
	if ce == nil || len(ce.Assigns) != 0 || len(redirs) != 0 {
		return nil
	}
	if imp := bashppForeignImport(ce); imp != nil {
		return imp
	}
	if len(ce.Args) != 2 && len(ce.Args) != 3 {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	if kw == nil || kw.Value != "import" {
		return nil
	}
	var alias *Lit
	pathAt := 1
	if len(ce.Args) == 3 {
		alias = bashppBareLit(ce.Args[1])
		if alias == nil || !bashppImportAlias(alias.Value) {
			return nil
		}
		pathAt = 2
	}
	q, ok := exactGoImportString(ce.Args[pathAt])
	if !ok {
		return nil
	}
	path, err := strconv.Unquote(`"` + q.Parts[0].(*Lit).Value + `"`)
	if err != nil || !validBashPPImportPath(path) {
		return nil
	}
	return &BashPPImport{Site: StartImport, Class: ClassE, Kw: kw, Alias: alias, Path: q}
}

// bashppForeignImport recognizes only the explicit Python form. In particular,
// a malformed prefix is left as an ordinary shell command.
func bashppForeignImport(ce *CallExpr) *BashPPImport {
	if len(ce.Args) != 3 && len(ce.Args) != 5 {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	if kw == nil || kw.Value != "import" {
		return nil
	}
	language, environment, lbrack, rbrack, ok := bashppForeignLanguage(ce.Args[1])
	if !ok || language.Value != "python" {
		return nil
	}
	path, ok := exactGoImportString(ce.Args[2])
	if !ok {
		return nil
	}
	modulePath, err := strconv.Unquote(`"` + path.Parts[0].(*Lit).Value + `"`)
	if err != nil {
		return nil
	}
	if BashPPPythonFileImport(modulePath) {
		if !validPythonFilePath(modulePath) {
			return nil
		}
	} else if !validPythonImportPath(modulePath) {
		return nil
	}
	var as, alias *Lit
	if len(ce.Args) == 5 {
		as, alias = bashppBareLit(ce.Args[3]), bashppBareLit(ce.Args[4])
		if as == nil || as.Value != "as" || alias == nil || alias.Value == "_" || !bashppIsIdent(alias.Value) {
			return nil
		}
	} else if _, ok := BashPPDerivedImportAlias(modulePath); !ok {
		return nil
	}
	return &BashPPImport{
		Site: StartImport, Class: ClassE, Kw: kw, Language: language,
		Lbrack: lbrack, Environment: environment, Rbrack: rbrack,
		As: as, Alias: alias, Path: path,
	}
}

func bashppForeignLanguage(word *Word) (language, environment *Lit, lbrack, rbrack Pos, ok bool) {
	if word == nil || len(word.Parts) == 0 {
		return nil, nil, Pos{}, Pos{}, false
	}
	var text strings.Builder
	for _, part := range word.Parts {
		lit, ok := part.(*Lit)
		if !ok {
			return nil, nil, Pos{}, Pos{}, false
		}
		text.WriteString(lit.Value)
	}
	value := text.String()
	if value == "python" {
		return &Lit{ValuePos: word.Pos(), ValueEnd: word.End(), Value: value}, nil, Pos{}, Pos{}, true
	}
	if !strings.HasPrefix(value, "python[") || !strings.HasSuffix(value, "]") {
		return nil, nil, Pos{}, Pos{}, false
	}
	name := value[len("python[") : len(value)-1]
	if name == "" {
		return nil, nil, Pos{}, Pos{}, false
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r)) {
			return nil, nil, Pos{}, Pos{}, false
		}
	}
	language = &Lit{ValuePos: word.Pos(), ValueEnd: posAddCol(word.Pos(), len("python")), Value: "python"}
	environment = &Lit{ValuePos: posAddCol(word.Pos(), len("python[")), ValueEnd: posAddCol(word.Pos(), len(value)-1), Value: name}
	return language, environment, language.End(), posAddCol(word.Pos(), len(value)-1), true
}

// BashPPPythonFileImport reports whether a Python import operand names a
// source file rather than a dotted module: it ends in `.py` (any case, as
// CPython's Windows finder accepts `.PY`) or carries a path separator. The
// polyglot package applies the same rule when planning; keep the two in step.
func BashPPPythonFileImport(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".py") || strings.ContainsAny(path, `/\`)
}

// validPythonFilePath accepts a file operand that names a .py source. A path
// without the suffix is refused here, as CPython's spec_from_file_location
// yields no spec for it; `import python "./x"` is not a shorthand.
func validPythonFilePath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".py") && len(path) > len(".py")
}

// bashppPythonFileStem is the file import's module name: the base name
// without its `.py` suffix, as SourceFileLoader names it.
func bashppPythonFileStem(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		path = path[i+1:]
	}
	return path[:len(path)-len(".py")]
}

// BashPPDerivedImportAlias returns the safe default binding for a Python
// module path. Callers must require an explicit alias when ok is false.
func BashPPDerivedImportAlias(modulePath string) (alias string, ok bool) {
	if BashPPPythonFileImport(modulePath) {
		if !validPythonFilePath(modulePath) {
			return "", false
		}
		stem := bashppPythonFileStem(modulePath)
		return stem, bashppIsIdent(stem)
	}
	if i := strings.LastIndexByte(modulePath, '.'); i >= 0 {
		modulePath = modulePath[i+1:]
	}
	return modulePath, bashppIsIdent(modulePath)
}

func validPythonImportPath(path string) bool {
	if path == "" || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return false
	}
	for _, part := range strings.Split(path, ".") {
		if !bashppIsIdent(part) {
			return false
		}
	}
	return true
}

func bashppImportAlias(name string) bool {
	return name == "_" || name == "." || bashppIsIdent(name)
}

// bashppImportGroup transactionally recognizes an exact Go import block.
// Rejection restores the ordinary Bash parser byte-for-byte.
func (p *Parser) bashppImportGroup(ce *CallExpr) Command {
	if ce == nil || len(ce.Assigns) != 0 || len(ce.Args) != 1 ||
		(p.tok != leftParen && p.tok != _Newl) {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	if kw == nil || kw.Value != "import" {
		return nil
	}
	txn := p.beginBashPPTxn()
	var comments []Comment
	if p.tok == _Newl {
		for p.tok == _Newl {
			p.next()
		}
		if p.tok != leftParen {
			txn.rollback(p)
			return nil
		}
		comments, p.accComs = p.accComs, nil
	}
	lparen := p.pos
	p.next()
	for p.tok == _Newl {
		p.next()
	}
	var specs []*BashPPImportSpec
	var last []Comment
	for {
		if p.tok == rightParen {
			last, p.accComs = p.accComs, nil
			break
		}
		specComments := p.accComs
		p.accComs = nil
		first := p.getWord()
		if first == nil {
			txn.rollback(p)
			return nil
		}
		var alias *Lit
		pathWord := first
		if _, ok := exactGoImportString(first); !ok {
			alias = bashppBareLit(first)
			if alias == nil || !bashppImportAlias(alias.Value) {
				txn.rollback(p)
				return nil
			}
			pathWord = p.getWord()
			if pathWord == nil {
				txn.rollback(p)
				return nil
			}
		}
		path, ok := exactGoImportString(pathWord)
		if !ok {
			txn.rollback(p)
			return nil
		}
		text, err := strconv.Unquote(`"` + path.Parts[0].(*Lit).Value + `"`)
		if err != nil || !validBashPPImportPath(text) {
			txn.rollback(p)
			return nil
		}
		specs = append(specs, &BashPPImportSpec{Comments: specComments, Alias: alias, Path: path})
		if p.tok == rightParen {
			continue
		}
		if p.tok != _Newl && p.tok != semicolon {
			txn.rollback(p)
			return nil
		}
		p.next()
		for p.tok == _Newl {
			p.next()
		}
	}
	rparen := p.pos
	p.next()
	if !bashppCallTerminator(p.tok) {
		txn.rollback(p)
		return nil
	}
	txn.commit(p)
	return &BashPPImport{
		Site: StartImport, Class: ClassR, Kw: kw, Comments: comments,
		Specs: specs, Last: last, Lparen: lparen, Rparen: rparen,
	}
}

func exactGoImportString(w *Word) (*DblQuoted, bool) {
	if w == nil || len(w.Parts) != 1 {
		return nil, false
	}
	q, ok := w.Parts[0].(*DblQuoted)
	if !ok || q.Dollar || len(q.Parts) != 1 {
		return nil, false
	}
	_, ok = q.Parts[0].(*Lit)
	return q, ok
}

func isGo127StdlibImport(path string) bool {
	_, ok := slices.BinarySearch(go127StdlibImports[:], path)
	return ok
}

// BashPPStdlibImportAllowed reports whether path is in the reviewed Go 1.27
// standard-library inventory. The interpreter uses this after go list has
// identified a package as standard; non-standard packages are instead subject
// to the selected module, workspace, vendor, or GOPATH resolver.
func BashPPStdlibImportAllowed(path string) bool { return isGo127StdlibImport(path) }

func validBashPPImportPath(path string) bool {
	return module.CheckImportPath(path) == nil
}
