package lower

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// Scalar ingress accepts the shell's textual representation as well as native
// values. Dynamic calls and direct Python imports do not use this adapter.
func foreignArgumentGoType(kind string, variadic bool) string {
	if variadic {
		return foreignGoType(kind)
	}
	switch kind {
	case "int", "float64", "bool":
		return "any"
	}
	return foreignGoType(kind)
}

func (e *emitter) foreignQualifiedName(module string, export polyglot.Export) string {
	for i, plan := range e.foreignPlans {
		if module == fmt.Sprintf("%sforeign%d", e.prefix, i) && plan.Alias != "" {
			return plan.Alias + "." + export.Name
		}
	}
	return export.Name
}

// Go evaluates the argument expressions once, in order, before entering the
// wrapper. Convert those captured values before dispatch; a bad input must not
// call the worker or become an infrastructure error in the opt-in form.
func (e *emitter) foreignScalarArguments(export polyglot.Export, qualified string, optIn bool) string {
	if export.Signature.Dynamic || export.Signature.Variadic {
		return ""
	}
	var out strings.Builder
	for i, kind := range export.Signature.Params {
		if kind != "int" && kind != "float64" && kind != "bool" {
			continue
		}
		fmt.Fprintf(&out, "arg%d, argErr%d := %srt.ForeignScalarArgument(arg%d, %q)\nif argErr%d != nil {\n", i, i, e.prefix, i, kind, i)
		fmt.Fprintf(&out, "argErr%d = %sfmt.Errorf(%q, argErr%d)\n", i, e.prefix, qualified+fmt.Sprintf(" argument %d: %%w", i+1), i)
		var zeros []string
		for _, kind := range export.Signature.Results {
			zeros = append(zeros, foreignZero(kind))
		}
		if optIn {
			fmt.Fprintf(&out, "%srt.Status = 0\n", e.prefix)
			zeros = append(zeros, fmt.Sprintf("%srt.TrustedErrorText(argErr%d.Error())", e.prefix, i))
		} else {
			fmt.Fprintf(&out, "%srt.Fail(%sfmt.Errorf(%q, argErr%d))\n", e.prefix, e.prefix, "bash++: foreign call "+qualified+" failed: %w", i)
			if e.execution {
				fmt.Fprintf(&out, "%sforeignProgram.SetStatus(1)\n", e.prefix)
			} else {
				fmt.Fprintf(&out, "%srt.Status = 1\n", e.prefix)
			}
		}
		fmt.Fprintf(&out, "return %s\n}\n", strings.Join(zeros, ","))
	}
	return out.String()
}

// Only direct calls use permissive scalar ingress. The exported method/function
// keeps its declared Go signature so taking or passing its value stays typed.
func foreignScalarAdapter(export polyglot.Export) bool {
	if export.Signature.Dynamic || export.Signature.Variadic {
		return false
	}
	for _, kind := range export.Signature.Params {
		if kind == "int" || kind == "float64" || kind == "bool" {
			return true
		}
	}
	return false
}

func foreignScalarAdapterName(module string, export polyglot.Export) string {
	return module + "_call_" + export.Name
}

func (e *emitter) foreignTypedTrampoline(receiver, module string, export polyglot.Export) string {
	var params, args, results []string
	if e.execution {
		params = append(params, e.prefix+"foreignProgram *"+e.prefix+"foreignProgramState")
		args = append(args, e.prefix+"foreignProgram")
	}
	for i, kind := range export.Signature.Params {
		name := fmt.Sprintf("arg%d", i)
		params = append(params, name+" "+foreignGoType(kind))
		args = append(args, name)
	}
	for _, kind := range export.Signature.Results {
		results = append(results, foreignGoType(kind))
	}
	signature, ret := "", ""
	if len(results) > 0 {
		signature = " (" + strings.Join(results, ",") + ")"
		ret = "return "
	}
	return fmt.Sprintf("func %s%s(%s)%s { %s%s(%s) }\n", receiver, export.Name, strings.Join(params, ","), signature, ret, foreignScalarAdapterName(module, export), strings.Join(args, ","))
}
