package gosource

import (
	"go/constant"
	"go/token"
	"strings"

	s "github.com/bashsharp/bashsharp/engine/syntax"
)

// Retain exact real and imaginary components, including rational constants.
// No float conversion occurs until the interpreter assigns a runtime type.
func (c *converter) complexConstantExpr(pos token.Pos, value constant.Value) s.BashPPExpr {
	imag := &s.BashPPBinaryExpr{X: c.floatConstantExpr(pos, constant.Imag(value)), Op: c.lit(pos, "*"), Y: &s.BashPPBasicLit{Kind: "IMAG", Value: c.lit(pos, "1i")}}
	return &s.BashPPBinaryExpr{X: c.floatConstantExpr(pos, constant.Real(value)), Op: c.lit(pos, "+"), Y: imag}
}

// floatConstantExpr spells an exact rational as scalar syntax. A leading sign
// is an operator, never part of a Go literal: constant.MakeFromLiteral rejects
// strings such as "-2.0" as Unknown.
func (c *converter) floatConstantExpr(pos token.Pos, value constant.Value) s.BashPPExpr {
	negative := constant.Sign(value) < 0
	if negative {
		value = constant.UnaryOp(token.SUB, value, 0)
	}
	parts := strings.Split(value.ExactString(), "/")
	lit := func(text string) *s.BashPPBasicLit {
		return &s.BashPPBasicLit{Kind: "FLOAT", Value: c.lit(pos, text+".0")}
	}
	var expr s.BashPPExpr = lit(parts[0])
	if len(parts) == 2 {
		expr = &s.BashPPBinaryExpr{X: expr, Op: c.lit(pos, "/"), Y: lit(parts[1])}
	}
	if negative {
		expr = &s.BashPPUnaryExpr{Op: c.lit(pos, "-"), X: expr}
	}
	return expr
}

func (c *converter) constantValueExpr(pos token.Pos, value constant.Value) s.BashPPExpr {
	switch value.Kind() {
	case constant.Bool:
		return &s.BashPPIdent{Name: c.lit(pos, value.ExactString())}
	case constant.String:
		return &s.BashPPBasicLit{Kind: "STRING", Value: c.lit(pos, value.ExactString())}
	case constant.Int:
		text := value.ExactString()
		if strings.HasPrefix(text, "-") {
			return &s.BashPPUnaryExpr{Op: c.lit(pos, "-"), X: &s.BashPPBasicLit{Kind: "INT", Value: c.lit(pos, text[1:])}}
		}
		return &s.BashPPBasicLit{Kind: "INT", Value: c.lit(pos, text)}
	case constant.Float:
		return c.floatConstantExpr(pos, value)
	case constant.Complex:
		return c.complexConstantExpr(pos, value)
	}
	return nil
}
