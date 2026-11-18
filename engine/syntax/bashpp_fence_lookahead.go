package syntax

import "strings"

// bashppFenceLookahead maps a fence language spelling — canonical and alias
// alike — to the reader that extracts `name(` from one body line when the
// line declares a top-level function, and "" otherwise. It exists only so an
// unaliased fence's later bare `name()` parses as a call; the language's
// analyzer decides what is really exported. Text fences have no row: they
// are always called through their alias and promote nothing.
var bashppFenceLookahead = map[string]func(line string) string{}

func bashppRegisterFenceLookahead(read func(line string) string, spellings ...string) {
	for _, spelling := range spellings {
		bashppFenceLookahead[spelling] = read
	}
}

func init() {
	bashppRegisterFenceLookahead(func(line string) string {
		if strings.HasPrefix(line, "def ") {
			return strings.TrimPrefix(line, "def ")
		}
		return ""
	}, "python", "py")
	bashppRegisterFenceLookahead(func(line string) string {
		declaration := strings.TrimPrefix(line, "export ")
		if !strings.HasPrefix(declaration, "function ") {
			return ""
		}
		return strings.TrimPrefix(declaration, "function ")
	}, "typescript", "ts", "javascript", "js")
	bashppRegisterFenceLookahead(func(line string) string {
		declaration := strings.TrimSpace(line)
		if !strings.HasPrefix(declaration, "pub fn ") {
			return ""
		}
		return strings.TrimPrefix(declaration, "pub fn ")
	}, "rust", "rs")
	bashppRegisterFenceLookahead(func(line string) string {
		declaration := strings.TrimSpace(line)
		if strings.HasPrefix(declaration, "static ") || strings.HasPrefix(declaration, "#") {
			return ""
		}
		before, after, ok := strings.Cut(declaration, "(")
		if !ok || before == "" {
			return ""
		}
		head := strings.TrimSpace(before)
		if strings.HasSuffix(head, "if") || strings.HasSuffix(head, "for") || strings.HasSuffix(head, "while") {
			return ""
		}
		parts := strings.Fields(before)
		if len(parts) <= 1 {
			return ""
		}
		return parts[len(parts)-1] + "(" + after
	}, "c", "cpp", "cxx")
	bashppRegisterFenceLookahead(func(line string) string {
		declaration := strings.TrimSpace(line)
		if !strings.HasPrefix(declaration, "func ") {
			return ""
		}
		rest := strings.TrimPrefix(declaration, "func ")
		// Generic declarations remain recognizable call names even when the
		// bridge will reject them. Do not mistake a parameter's slice type
		// for the function's type-parameter list.
		if bracket := strings.IndexByte(rest, '['); bracket >= 0 {
			if paren := strings.IndexByte(rest, '('); paren < 0 || bracket < paren {
				return strings.TrimSpace(rest[:bracket]) + "("
			}
		}
		return rest
	}, "go")
	// A PowerShell function header is `function Name {` (or `filter Name {`),
	// with an optional scope modifier (`global:Name`). Only a name that is a
	// valid Bash# identifier promotes to a bare call; a Verb-Noun name like
	// `Get-Item` carries a hyphen and is rejected here, so it is reached only
	// through the string-keyed call form (S358.0), never a bareword.
	bashppRegisterFenceLookahead(func(line string) string {
		declaration := strings.TrimSpace(line)
		rest := ""
		for _, kw := range []string{"function ", "filter "} {
			if r, ok := strings.CutPrefix(declaration, kw); ok {
				rest = strings.TrimSpace(r)
				break
			}
		}
		if rest == "" {
			return ""
		}
		name := rest
		if i := strings.IndexAny(rest, " \t({"); i >= 0 {
			name = rest[:i]
		}
		if i := strings.LastIndex(name, ":"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			return ""
		}
		return name + "("
	}, "powershell", "pwsh", "ps1")
	// A C# export is a `public static` method; `name(` follows its return type.
	// The analyzer reflects over the compiled assembly and stays authoritative.
	bashppRegisterFenceLookahead(func(line string) string {
		declaration := strings.TrimSpace(line)
		if !strings.HasPrefix(declaration, "public static ") {
			return ""
		}
		before, after, ok := strings.Cut(declaration, "(")
		if !ok {
			return ""
		}
		fields := strings.Fields(before)
		if len(fields) < 4 || strings.ContainsAny(before, "=;") {
			return ""
		}
		return fields[len(fields)-1] + "(" + after
	}, "csharp", "cs")
	bashppRegisterFenceLookahead(func(line string) string {
		before, _, ok := strings.Cut(strings.TrimSpace(line), "()")
		if !ok {
			return ""
		}
		return strings.TrimSpace(before) + "("
	}, "bash", "sh")
}
