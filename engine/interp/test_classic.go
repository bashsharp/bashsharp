// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const illegalTok = 0

type testParser struct {
	eof    bool
	val    string
	rem    []string
	errd   bool   // an error was already reported; bail subsequent diagnostics
	closer string // the literal token that closes the test (`]` for `[`, empty for plain test)

	err func(err error)
}

func (p *testParser) errf(format string, a ...any) {
	if p.errd {
		return
	}
	p.errd = true
	p.err(fmt.Errorf(format, a...))
}

func (p *testParser) next() {
	if p.eof || len(p.rem) == 0 {
		p.eof = true
		p.val = ""
		return
	}
	p.val = p.rem[0]
	p.rem = p.rem[1:]
}

func (p *testParser) followWord(fval string) *syntax.Word {
	if p.eof {
		// Bash 5.3 wording for "binary op missing right operand"
		// is `syntax error: \`OP' unexpected` for the comparison
		// ops, and `argument expected` for the boolean -a / -o.
		// followWord is only reached for the comparison ops; the
		// -a/-o path emits its own diagnostic via classicTest.
		p.errf("syntax error: `%s' unexpected", fval)
	}
	w := &syntax.Word{Parts: []syntax.WordPart{
		&syntax.Lit{Value: p.val},
	}}
	p.next()
	return w
}

func (p *testParser) classicTest(fval string, pastAndOr bool) syntax.TestExpr {
	var left syntax.TestExpr
	if pastAndOr {
		left = p.testExprBase(fval)
	} else {
		left = p.classicTest(fval, true)
	}
	if left == nil || p.eof || p.val == ")" {
		return left
	}
	opStr := p.val
	op := testBinaryOp(p.val)
	if op == illegalTok {
		// Bash 5.3 wording: a leading `-` (looks like an
		// operator name) gets `syntax error: \`X' unexpected`;
		// anything else gets `too many arguments`.
		if strings.HasPrefix(p.val, "-") {
			p.errf("syntax error: `%s' unexpected", p.val)
		} else {
			p.errf("too many arguments")
		}
	}
	b := &syntax.BinaryTest{
		Op: op,
		X:  left,
	}
	p.next()
	switch b.Op {
	case syntax.AndTest, syntax.OrTest:
		if b.Y = p.classicTest(opStr, false); b.Y == nil {
			// Bash 5.3 emits `argument expected` rather than
			// naming the boolean operator.
			p.errf("argument expected")
		}
	default:
		b.Y = p.followWord(opStr)
	}
	return b
}

func (p *testParser) testExprBase(fval string) syntax.TestExpr {
	if p.eof || p.val == ")" {
		return nil
	}
	op := testUnaryOp(p.val)
	switch op {
	case syntax.TsNot:
		u := &syntax.UnaryTest{Op: op}
		p.next()
		u.X = p.classicTest(op.String(), false)
		return u
	case syntax.TsParen:
		p.next()
		// Bash 5.3: a lone `(` with no closing `)` is the 1-arg form — `(`
		// is a literal non-empty string (true), as in `[ ( ]`. (An empty
		// group `( )` is still a syntax error, handled below.)
		if p.eof {
			return &syntax.Word{Parts: []syntax.WordPart{
				&syntax.Lit{Value: op.String()},
			}}
		}
		pe := &syntax.ParenTest{}
		pe.X = p.classicTest(op.String(), false)
		// Bash 5.3: `( )` with an empty body is a syntax error
		// (the `)' is found in the place where the argument
		// should be). Surface that as `\`)' expected`.
		if pe.X == nil && p.val == ")" {
			p.errf("`)' expected")
		}
		if p.val != ")" {
			// bash: `\`)' expected` (or `\`)' expected, found X`
			// when something else was there). When p.eof is set
			// AND there's a closer token (`]` for `[`), use it
			// in the diagnostic.
			switch {
			case !p.eof:
				p.errf("`)' expected, found %s", p.val)
			case p.closer != "":
				p.errf("`)' expected, found %s", p.closer)
			default:
				p.errf("`)' expected")
			}
		}
		p.next()
		return pe
	case illegalTok:
		return p.followWord(fval)
	default:
		u := &syntax.UnaryTest{Op: op}
		p.next()
		// Bash 5.3: a unary operator at the end of args, OR followed
		// by `)` (group closer) without an explicit operand, falls
		// back to the 1-arg form — the operator is treated as a
		// literal non-empty string (true). Covers `[ -e ]`,
		// `( ! -a )`, etc.
		if p.eof || p.val == ")" {
			return &syntax.Word{Parts: []syntax.WordPart{
				&syntax.Lit{Value: op.String()},
			}}
		}
		// Bash 5.3: when a unary operator is followed by `-a` or `-o`
		// (which double as the binary AND/OR operators) AND there are
		// more tokens after that, resolve the ambiguity in favor of the
		// binary interpretation: the unary operator becomes the 1-arg
		// form (literal non-empty string) and -a/-o acts as the binary
		// operator. Covers `[ -z -a ] ]` (exit 0) and `[ -z -a -a ]`.
		if (p.val == "-a" || p.val == "-o") && len(p.rem) > 0 {
			return &syntax.Word{Parts: []syntax.WordPart{
				&syntax.Lit{Value: op.String()},
			}}
		}
		u.X = p.followWord(op.String())
		return u
	}
}

// testUnaryOp is an exact copy of syntax's.
func testUnaryOp(val string) syntax.UnTestOperator {
	switch val {
	case "!":
		return syntax.TsNot
	case "(":
		return syntax.TsParen
	case "-e", "-a":
		return syntax.TsExists
	case "-f":
		return syntax.TsRegFile
	case "-d":
		return syntax.TsDirect
	case "-c":
		return syntax.TsCharSp
	case "-b":
		return syntax.TsBlckSp
	case "-p":
		return syntax.TsNmPipe
	case "-S":
		return syntax.TsSocket
	case "-L", "-h":
		return syntax.TsSmbLink
	case "-k":
		return syntax.TsSticky
	case "-g":
		return syntax.TsGIDSet
	case "-u":
		return syntax.TsUIDSet
	case "-G":
		return syntax.TsGrpOwn
	case "-O":
		return syntax.TsUsrOwn
	case "-N":
		return syntax.TsModif
	case "-r":
		return syntax.TsRead
	case "-w":
		return syntax.TsWrite
	case "-x":
		return syntax.TsExec
	case "-s":
		return syntax.TsNoEmpty
	case "-t":
		return syntax.TsFdTerm
	case "-z":
		return syntax.TsEmpStr
	case "-n":
		return syntax.TsNempStr
	case "-o":
		return syntax.TsOptSet
	case "-v":
		return syntax.TsVarSet
	case "-R":
		return syntax.TsRefVar
	default:
		return illegalTok
	}
}

// testBinaryOp is like syntax's, but with -a and -o, and without =~.
func testBinaryOp(val string) syntax.BinTestOperator {
	switch val {
	case "-a":
		return syntax.AndTest
	case "-o":
		return syntax.OrTest
	case "==", "=":
		return syntax.TsMatch
	case "!=":
		return syntax.TsNoMatch
	case "-nt":
		return syntax.TsNewer
	case "-ot":
		return syntax.TsOlder
	case "-ef":
		return syntax.TsDevIno
	case "-eq":
		return syntax.TsEql
	case "-ne":
		return syntax.TsNeq
	case "-le":
		return syntax.TsLeq
	case "-ge":
		return syntax.TsGeq
	case "-lt":
		return syntax.TsLss
	case "-gt":
		return syntax.TsGtr
	case "<":
		return syntax.TsBefore
	case ">":
		return syntax.TsAfter
	default:
		return illegalTok
	}
}
