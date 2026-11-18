// Copyright (c) 2026, the bashy fork
// See LICENSE for licensing information

package syntax

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

// DbracketParseError reports whether err is a `[[ ]]` conditional-command
// parse error and, if so, renders it the way bash 5.3 reports such errors.
//
// bash emits up to three lines for a malformed conditional, each prefixed
// with "<name>: line <n>: ":
//
//	<name>: line N: <primary diagnostic>          (omitted for some forms)
//	<name>: line N: syntax error near `<token>'
//	<name>: line N: `<offending source line>'
//
// This logic used to live only in the gosh CLI, so the bash drop-in CLIs
// built on this engine could not reproduce it. Keeping it here lets every
// consumer turn a [[ ]] ParseError into bash's wording without duplicating
// the heuristics. name is the script name bash would print (e.g. "./s" or
// "bash: -c"); src is the script source the error came from.
//
// It returns ok=false when err is not a ParseError pointing at a `[[ ]]`
// construct, so callers can fall back to their generic ParseError handling.
func DbracketParseError(err error, src []byte, name string) (msg string, ok bool) {
	var pe ParseError
	if !errors.As(err, &pe) {
		return "", false
	}
	line := int(pe.Pos.Line())
	stmtLine, stmt := dbracketSourceLine(src, line)
	if stmtLine == 0 {
		return "", false
	}
	prefix := name + ": line " + strconv.Itoa(stmtLine) + ": "

	// A `[[` that never reached its `]]`. Bash reports these as a pair --
	// a diagnostic plus the generic unclosed-command line -- with no echo
	// of the source line, and it blames the end of input rather than the
	// `[[`. An expression left dangling mid-operator is a different shape
	// and is handled by dbracketFirstDiagnostic instead.
	if pe.Incomplete {
		fields := strings.Fields(strings.TrimSpace(stmt))
		eofLine := bytes.Count(src, []byte("\n")) + 1
		if strings.TrimSpace(dbracketLine(src, eofLine)) != "" {
			eofLine++
		}
		unclosed := name + ": line " + strconv.Itoa(eofLine) +
			": syntax error: unexpected end of file from `[[' command on line " +
			strconv.Itoa(stmtLine)
		switch {
		case pe.Text == "`[[` must be followed by an expression" && len(fields) == 1:
			return name + ": line " + strconv.Itoa(eofLine) +
				": unexpected token `EOF' in conditional command\n" + unclosed, true
		case pe.Text == "unexpected EOF while looking for matching `]]'" && len(fields) != 2:
			return prefix + "unexpected EOF while looking for `]]'\n" + unclosed, true
		}
	}

	near := dbracketNearToken(pe, stmt)
	var b strings.Builder
	if first := dbracketFirstDiagnostic(pe, stmt); first != "" {
		b.WriteString(prefix)
		b.WriteString(first)
		b.WriteByte('\n')
	}
	b.WriteString(prefix)
	b.WriteString("syntax error near `")
	b.WriteString(near)
	b.WriteString("'\n")
	b.WriteString(prefix)
	b.WriteByte('`')
	b.WriteString(stmt)
	b.WriteByte('\'')
	return b.String(), true
}

func dbracketLine(src []byte, line int) string {
	if line < 1 {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	if line > len(lines) {
		return ""
	}
	return lines[line-1]
}

// dbracketSourceLine walks back from the error line to the line that opens
// the `[[` construct, since the parser may report an error on a later line
// (e.g. when an operand greedily consumed the `]]` terminator).
func dbracketSourceLine(src []byte, line int) (int, string) {
	for n := line; n >= 1; n-- {
		stmt := dbracketLine(src, n)
		if strings.HasPrefix(strings.TrimLeft(stmt, " \t"), "[[") {
			return n, stmt
		}
	}
	return 0, ""
}

func dbracketFirstDiagnostic(pe ParseError, stmt string) string {
	text := pe.Text
	trim := strings.TrimSpace(stmt)
	fields := strings.Fields(trim)
	switch {
	// End of input inside `[[ ... ]]`. Bash names the newline token it
	// reached, and which diagnostic it picks depends on what the
	// expression was still waiting for:
	//
	//	[[ a      an operand, so a binary operator was expected
	//	[[ -n     an argument for a unary operator
	//	[[ a ==   an argument for a binary operator
	//
	// A complete expression merely missing its `]]` (`[[ a == b`) is a
	// different shape entirely and is left alone here.
	case pe.Incomplete && text == "unexpected EOF while looking for matching `]]'" && len(fields) == 2:
		return "unexpected token `newline', conditional binary operator expected"
	case pe.Incomplete && strings.Contains(text, "must be followed by a word") && len(fields) == 2:
		return "unexpected argument `newline' to conditional unary operator"
	case pe.Incomplete && strings.Contains(text, "must be followed by a word") && len(fields) == 3 &&
		strings.HasSuffix(trim, fields[2]):
		return "unexpected argument `newline' to conditional binary operator"
	case text == "`[[` must be followed by an expression" && strings.HasPrefix(trim, "[[ &&"):
		return "unexpected token `&&' in conditional command"
	case text == "`[[` must be followed by an expression":
		return ""
	case strings.Contains(text, "must be followed by a word"):
		op := strings.TrimPrefix(text, "`")
		if i := strings.IndexByte(op, '`'); i >= 0 {
			op = op[:i]
		}
		if len(fields) >= 3 && fields[0] == "[[" && fields[1] == op && fields[2] == "]]" {
			return "unexpected argument `]]' to conditional unary operator"
		}
		if len(fields) >= 3 && fields[0] == "[[" && fields[1] == op {
			return "unexpected argument `" + fields[2] + "' to conditional unary operator"
		}
	case strings.Contains(text, "not a valid test operator"):
		if len(fields) == 3 && fields[0] == "[[" && (fields[1] == "-z" || fields[1] == "-n") && fields[2] == "]]" {
			return "unexpected argument `]]' to conditional unary operator"
		}
		if len(fields) >= 4 && fields[0] == "[[" {
			if fields[1] == "-z" || fields[1] == "-n" {
				return "syntax error in conditional expression: unexpected token `" + fields[3] + "'"
			}
			return "unexpected token `" + condBinOpToken(fields[2]) + "', conditional binary operator expected"
		}
		// `[[ a ]` -- the operand is followed by something that is not a
		// binary operator and the expression ends there. Bash still names
		// the offending token.
		if len(fields) == 3 && fields[0] == "[[" {
			return "unexpected token `" + condBinOpToken(fields[2]) + "', conditional binary operator expected"
		}
		return "conditional binary operator expected"
	}
	return text
}

// condBinOpToken returns the fragment bash names as the "unexpected token"
// when it finds tok where a conditional binary operator was expected. A
// redirection-shaped token such as `3<` / `3>` is reported by its leading
// file-descriptor digits alone (`3`), even though the "syntax error near"
// line still echoes the whole `3<`.
func condBinOpToken(tok string) string {
	for i := 0; i < len(tok); i++ {
		if tok[i] >= '0' && tok[i] <= '9' {
			continue
		}
		if i > 0 && (tok[i] == '<' || tok[i] == '>') {
			return tok[:i]
		}
		break
	}
	return tok
}

func dbracketNearToken(pe ParseError, stmt string) string {
	fields := strings.Fields(stmt)
	text := pe.Text
	if text == "`[[` must be followed by an expression" {
		if len(fields) >= 2 && fields[1] == "&&" {
			return "&"
		}
		return "]]"
	}
	if strings.Contains(text, "must be followed by a word") {
		if len(fields) >= 3 {
			return strings.Trim(fields[2], "'\"")
		}
		// `[[ -n` -- the dangling unary operator is the last field.
		if len(fields) == 2 {
			return strings.Trim(fields[1], "'\"")
		}
	}
	// `[[ a` -- the dangling operand bash reports as the near token.
	if pe.Incomplete && text == "unexpected EOF while looking for matching `]]'" && len(fields) == 2 {
		return strings.Trim(fields[1], "'\"")
	}
	if strings.Contains(text, "not a valid test operator") && len(fields) >= 3 {
		if len(fields) == 3 && fields[2] == "]]" {
			return "]]"
		}
		if len(fields) >= 4 && (fields[1] == "-z" || fields[1] == "-n") {
			return strings.Trim(fields[3], "'\"")
		}
		op := fields[2]
		if strings.HasPrefix(op, "3<") || strings.HasPrefix(op, "3>") {
			return op
		}
		return strings.Trim(op, "'\"")
	}
	if col := int(pe.Pos.Col()); col > 0 && col <= len(stmt) {
		rest := stmt[col-1:]
		if tok := strings.Fields(rest); len(tok) > 0 {
			return strings.Trim(tok[0], "'\"")
		}
	}
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
