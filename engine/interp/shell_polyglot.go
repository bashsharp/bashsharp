package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The dialect islands are fence language rows like every other language;
// the interpreter owns their runtime, so it registers them. A lowered
// program constructs the same runtime from a process-environment snapshot.
func init() {
	for _, language := range []string{"bash", "sh"} {
		polyglot.RegisterLanguage(polyglot.Language{
			Canonical: language,
			NewRuntime: func(cfg polyglot.RuntimeConfig) polyglot.LanguageRuntime {
				return ShellRuntime(language, cfg.Dir, cfg.Environ)
			},
			LoweredRuntime: func(prefix, _ string) string {
				return fmt.Sprintf("%sinterp.ShellRuntime(%q,\"\",nil)", prefix, language)
			},
			LoweredImports: []string{"github.com/bashsharp/bashsharp/engine/interp"},
		})
	}
}

// ShellRuntime returns the embedded runtime for a Bash or POSIX dialect
// island. Every call constructs a fresh Runner from the captured environment;
// no host shell or worker process is involved.
func ShellRuntime(language, dir string, environ []string) polyglot.Embedded {
	variant, label := syntax.LangBash, "Bash"
	if language == "sh" {
		variant, label = syntax.LangPOSIX, "POSIX sh"
	}
	if environ == nil {
		environ = os.Environ()
	}
	env := append([]string(nil), environ...)
	return polyglot.Embedded{
		RuntimeName: label,
		AnalyzeFunc: func(ctx context.Context, source string) ([]polyglot.Export, error) {
			return analyzeShellIsland(ctx, source, variant)
		},
		CallFunc: func(ctx context.Context, plan polyglot.Plan, name string, args []any, kwargs map[string]any) (polyglot.CallResult, error) {
			return callShellIsland(ctx, plan, name, args, kwargs, variant, dir, env)
		},
	}
}

func analyzeShellIsland(ctx context.Context, source string, variant syntax.LangVariant) ([]polyglot.Export, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(source), "<shell fence>")
	if err != nil {
		return nil, err
	}
	exports := make([]polyglot.Export, 0, len(file.Stmts))
	for _, stmt := range file.Stmts {
		decl, ok := stmt.Cmd.(*syntax.FuncDecl)
		if !ok {
			return nil, fmt.Errorf("%s: only top-level function declarations are allowed", stmt.Pos())
		}
		name := decl.Name.Value
		if strings.HasPrefix(name, "_") || !syntax.BashPPValidIdent(name) {
			continue
		}
		exports = append(exports, polyglot.Export{Name: name, Signature: polyglot.Signature{
			Params: []string{"string"}, Results: []string{"string"}, Variadic: true,
		}})
	}
	return exports, nil
}

func callShellIsland(ctx context.Context, plan polyglot.Plan, name string, args []any, kwargs map[string]any, variant syntax.LangVariant, dir string, environ []string) (polyglot.CallResult, error) {
	if len(kwargs) != 0 {
		return polyglot.CallResult{}, fmt.Errorf("shell function %s does not accept keyword arguments", name)
	}
	found := false
	for _, export := range plan.Exports {
		if export.Name == name {
			found = true
			break
		}
	}
	if !found {
		return polyglot.CallResult{}, fmt.Errorf("shell function %s is not exported", name)
	}
	var source strings.Builder
	source.WriteString(plan.Source)
	if !strings.HasSuffix(plan.Source, "\n") {
		source.WriteByte('\n')
	}
	source.WriteString(name)
	for _, arg := range args {
		value, ok := arg.(string)
		if !ok {
			return polyglot.CallResult{}, fmt.Errorf("shell function %s argument is %T, want string", name, arg)
		}
		quoted, err := syntax.Quote(value, variant)
		if err != nil {
			return polyglot.CallResult{}, fmt.Errorf("shell function %s argument: %w", name, err)
		}
		source.WriteByte(' ')
		source.WriteString(quoted)
	}
	source.WriteByte('\n')
	file, err := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(source.String()), "<shell fence call>")
	if err != nil {
		return polyglot.CallResult{}, err
	}
	var stdout, stderr bytes.Buffer
	options := []RunnerOption{Lang(variant), Env(expand.ListEnviron(environ...)), StdIO(nil, &stdout, &stderr)}
	if dir != "" {
		options = append(options, Dir(dir))
	}
	runner, err := New(options...)
	if err != nil {
		return polyglot.CallResult{}, err
	}
	err = runner.Run(ctx, file)
	result := polyglot.CallResult{Value: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if status, ok := IsExitStatus(err); ok {
			return result, fmt.Errorf("shell function %s exited with status %d", name, status)
		}
		return result, err
	}
	return result, nil
}
