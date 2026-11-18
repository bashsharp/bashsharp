package interp_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runWithResolver runs src on a fresh runner whose CommandResolver knows the
// two names an embedder might dispatch itself: `hi` (no path — a script
// body the embedder re-enters) and `gl` (a path — an exec record).
func runWithResolver(t *testing.T, src string, opts ...interp.RunnerOption) (string, error) {
	t.Helper()
	resolve := func(name string) (interp.ResolvedCommand, bool) {
		switch name {
		case "hi":
			return interp.ResolvedCommand{Desc: "hi is a registered command (script)"}, true
		case "gl":
			return interp.ResolvedCommand{Desc: "gl is a registered command (exec)", Path: "/opt/bin/gl"}, true
		}
		return interp.ResolvedCommand{}, false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opts = append([]interp.RunnerOption{interp.StdIO(nil, &out, &out), interp.CommandResolver(resolve)}, opts...)
	r, err := interp.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	err = r.Run(ctx, file)
	return out.String(), err
}

func TestCommandResolverIntrospection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src  string
		want string
	}{
		// type: the resolver's description, and -t the file kind.
		{`type hi`, "hi is a registered command (script)\n"},
		{`type -t hi`, "file\n"},
		{`type gl`, "gl is a registered command (exec)\n"},
		// command -v: the path when there is one, else the bare name — the
		// builtin/function shape, since the name runs when invoked.
		{`command -v hi`, "hi\n"},
		{`command -v gl`, "/opt/bin/gl\n"},
		{`command -V hi`, "hi is a registered command (script)\n"},
		// -p prints the file that would run: a path, or nothing.
		{`type -p gl`, "/opt/bin/gl\n"},
		{`type -p hi; echo "rc=$?"`, "rc=0\n"},
		// The shell's own names still outrank the resolver …
		{`hi() { echo fn; }; type -t hi`, "function\n"},
		{`alias hi=echo; type hi`, "hi is aliased to `echo'\n"},
		// … and a name the resolver does not know is unchanged.
		{`type nosuchthing 2>&1; echo "rc=$?"`, "type: nosuchthing: not found\nrc=1\n"},
		{`command -v nosuchthing; echo "rc=$?"`, "rc=1\n"},
		// A word with a slash is a path, never a resolver question.
		{`command -v ./hi; echo "rc=$?"`, "rc=1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			t.Parallel()
			got, _ := runWithResolver(t, tc.src, interp.Params("-O", "expand_aliases"))
			if got != tc.want {
				t.Fatalf("%s:\n got %q\nwant %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestCommandResolverPATHStaysPATH pins the two questions that are about
// FILES and therefore ignore the resolver: `type -P` and `hash`.
func TestCommandResolverPATHStaysPATH(t *testing.T) {
	t.Parallel()
	got, _ := runWithResolver(t, `type -P hi; echo "rc=$?"; hash hi 2>&1; echo "rc=$?"`, interp.Env(nil))
	want := "rc=1\nhash: hi: not found\nrc=1\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestCommandResolverSubshell pins that the resolver survives the subshell
// copy, like every other embedder hook.
func TestCommandResolverSubshell(t *testing.T) {
	t.Parallel()
	got, _ := runWithResolver(t, `(type -t hi); echo "$(command -v gl)"`)
	if want := "file\n/opt/bin/gl\n"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestNoCommandResolver pins the default: without a resolver the runner
// resolves exactly as bash does.
func TestNoCommandResolver(t *testing.T) {
	t.Parallel()
	file, _ := syntax.NewParser().Parse(strings.NewReader(`type -t hi; echo "rc=$?"`), "")
	var out bytes.Buffer
	r, _ := interp.New(interp.StdIO(nil, &out, &out))
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "rc=1\n" {
		t.Fatalf("got %q", got)
	}
}

func runRegisteredSchema(t *testing.T, src string, resolve interp.CommandResolverFunc) (stdout, stderr string, calls int, err error) {
	t.Helper()
	file, parseErr := syntax.NewParser().Parse(strings.NewReader(src), "")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	var out, errs bytes.Buffer
	handler := func(ctx context.Context, args []string) error {
		calls++
		fmt.Fprintln(&out, strings.Join(args, "|"))
		return nil
	}
	r, newErr := interp.New(
		interp.StdIO(nil, &out, &errs),
		interp.CommandResolver(resolve),
		interp.ExecHandler(handler),
	)
	if newErr != nil {
		t.Fatal(newErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	err = r.Run(ctx, file)
	return out.String(), errs.String(), calls, err
}

// TestRegisteredCommandSchemaBindValidateInput is native coverage (not a
// port) of the full bind/validate-input design: flags and positionals,
// short/long flag forms, type conversion, defaults, Enum validation,
// required-ness, and the too-many-positionals and unknown-flag cases. The
// upstream-derived ValidateSet and Parameter-binding cases live in
// TestRegisteredCommandSchemaPortedFromPowerShellValidateSet and
// TestRegisteredCommandSchemaPortedFromPowerShellParameterBinding below.
func TestRegisteredCommandSchemaBindValidateInput(t *testing.T) {
	t.Parallel()
	schema := &interp.CommandSchema{
		Flags: []interp.CommandFlag{
			{Name: "mode", Shorthand: "m", Type: "string", Default: "fast", Enum: []string{"fast", "slow"}},
			{Name: "count", Shorthand: "c", Type: "int", Required: true},
			{Name: "verbose", Shorthand: "v", Type: "bool"},
		},
		Positionals: []interp.CommandParameter{
			{Name: "color", Type: "string", Required: true, Enum: []string{"red", "blue"}},
			{Name: "level", Type: "int", Default: "3"},
		},
	}
	resolve := func(name string) (interp.ResolvedCommand, bool) {
		if name == "paint" {
			return interp.ResolvedCommand{Desc: "paint is a registered command (script)", Schema: schema}, true
		}
		return interp.ResolvedCommand{}, false
	}
	cases := []struct {
		name       string
		src        string
		wantOut    string
		wantErr    string
		wantCalls  int
		wantRunErr bool
	}{
		{
			name:      "positional named enum conversion and default",
			src:       `paint --mode slow -c 007 blue`,
			wantOut:   "paint|--mode=slow|--count=7|blue|3\n",
			wantCalls: 1,
		},
		{
			name:      "defaults and bool flag",
			src:       `paint --count=2 --verbose red`,
			wantOut:   "paint|--mode=fast|--count=2|--verbose=true|red|3\n",
			wantCalls: 1,
		},
		{
			name:      "missing required",
			src:       `paint red; echo after`,
			wantOut:   "after\n",
			wantErr:   "missing required flag --count",
			wantCalls: 0,
		},
		{
			name:      "extra positional",
			src:       `paint --count 1 red 2 extra; echo after`,
			wantOut:   "after\n",
			wantErr:   "too many positional arguments",
			wantCalls: 0,
		},
		{
			name:      "enum refused",
			src:       `paint --count 1 green; echo after`,
			wantOut:   "after\n",
			wantErr:   `color must be one of red, blue, got "green"`,
			wantCalls: 0,
		},
		{
			name:      "conversion error",
			src:       `paint --count nope red; echo after`,
			wantOut:   "after\n",
			wantErr:   `--count expects int, got "nope"`,
			wantCalls: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, calls, err := runRegisteredSchema(t, tc.src, resolve)
			if tc.wantRunErr && err == nil {
				t.Fatal("Run succeeded unexpectedly")
			}
			if !tc.wantRunErr && err != nil {
				t.Fatalf("Run: %v\nstderr=%s", err, stderr)
			}
			if stdout != tc.wantOut {
				t.Fatalf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("stderr = %q, want substring %q", stderr, tc.wantErr)
			}
			if tc.wantErr == "" && stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			if calls != tc.wantCalls {
				t.Fatalf("body calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestRegisteredCommandSchemaPortedFromPowerShellValidateSet ports two
// upstream ValidateSet acceptance/rejection assertions onto the Go schema's
// Enum field, translating a PowerShell cmdlet call
// (`Get-TestValidateSetPS4 -Param1 <value>` / `get-fook -p <value>`) into
// this package's positional-argument call (`<command> <value>`).
//
// Pinned upstream commit: PowerShell/PowerShell @
// 1e53f6bbab4b8791eae782474d21889f9e5d6038 (github.com/PowerShell/PowerShell),
// LICENSE.txt at the repo root is the MIT License.
//
//   - Source file: test/powershell/Language/Classes/Scripting.Classes.Attributes.Tests.ps1
//     Describe 'ValidateSet support a dynamically generated set' > Context
//     'Powershell tests'. The valid-value set ("Test1","TestString1","Test2")
//     comes from that Context's `class GenValuesForParam.GetValidValues()`.
//     Ported test/case names:
//
//   - It 'Dynamically generated set works in PowerShell script with
//     default (immediate) cache expire':
//     `Get-TestValidateSetPS4 -Param1 "TestString1" -ErrorAction
//     SilentlyContinue | Should -BeExactly "TestString1"`
//
//   - It 'Get the appropriate error message':
//     `{Get-TestValidateSetPS4 -Param1 "TestStringWrong" -ErrorAction Stop}
//     | Should -Throw -ErrorId
//     "ParameterArgumentValidationError,Get-TestValidateSetPS4"`
//
//   - Source file: test/powershell/Language/Scripting/ParameterBinding.Tests.ps1
//     Describe "Tests for parameter binding" > Context 'Default value
//     conversion tests'. Ported test/case name:
//
//   - It "ValidateSet can use custom ErrorMessage": enum ('A','B','C'),
//     `get-fook -p 2` throws `ParameterArgumentValidationError,get-fook`
//     with message "... Item '2' is not in '...'".
func TestRegisteredCommandSchemaPortedFromPowerShellValidateSet(t *testing.T) {
	t.Parallel()

	t.Run("valid value from the set is accepted", func(t *testing.T) {
		t.Parallel()
		schema := &interp.CommandSchema{
			Positionals: []interp.CommandParameter{
				{Name: "Param1", Type: "string", Required: true, Enum: []string{"Test1", "TestString1", "Test2"}},
			},
		}
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "get-testvalidatesetps4" {
				return interp.ResolvedCommand{Desc: "registered command (script)", Schema: schema}, true
			}
			return interp.ResolvedCommand{}, false
		}
		stdout, stderr, calls, err := runRegisteredSchema(t, `get-testvalidatesetps4 TestString1`, resolve)
		if err != nil {
			t.Fatalf("Run: %v\nstderr=%s", err, stderr)
		}
		if stdout != "get-testvalidatesetps4|TestString1\n" || stderr != "" || calls != 1 {
			t.Fatalf("stdout=%q stderr=%q calls=%d", stdout, stderr, calls)
		}
	})

	t.Run("value outside the set is rejected before the body runs", func(t *testing.T) {
		t.Parallel()
		schema := &interp.CommandSchema{
			Positionals: []interp.CommandParameter{
				{Name: "Param1", Type: "string", Required: true, Enum: []string{"Test1", "TestString1", "Test2"}},
			},
		}
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "get-testvalidatesetps4" {
				return interp.ResolvedCommand{Desc: "registered command (script)", Schema: schema}, true
			}
			return interp.ResolvedCommand{}, false
		}
		stdout, stderr, calls, err := runRegisteredSchema(t, `get-testvalidatesetps4 TestStringWrong; echo after`, resolve)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stdout != "after\n" || calls != 0 {
			t.Fatalf("stdout=%q calls=%d", stdout, calls)
		}
		if !strings.Contains(stderr, `must be one of Test1, TestString1, Test2, got "TestStringWrong"`) {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("custom error message case rejects the offending value", func(t *testing.T) {
		t.Parallel()
		schema := &interp.CommandSchema{
			Positionals: []interp.CommandParameter{
				{Name: "p", Type: "string", Required: true, Enum: []string{"A", "B", "C"}},
			},
		}
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "get-fook" {
				return interp.ResolvedCommand{Desc: "registered command (script)", Schema: schema}, true
			}
			return interp.ResolvedCommand{}, false
		}
		stdout, stderr, calls, err := runRegisteredSchema(t, `get-fook 2; echo after`, resolve)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stdout != "after\n" || calls != 0 {
			t.Fatalf("stdout=%q calls=%d", stdout, calls)
		}
		if !strings.Contains(stderr, `p must be one of A, B, C, got "2"`) {
			t.Fatalf("stderr = %q", stderr)
		}
	})
}

// TestRegisteredCommandSchemaPortedFromPowerShellParameterBinding ports two
// upstream Parameter-binding assertions.
//
// Pinned upstream commit: PowerShell/PowerShell @
// 1e53f6bbab4b8791eae782474d21889f9e5d6038 (github.com/PowerShell/PowerShell),
// LICENSE.txt at the repo root is the MIT License.
//
//   - Source file: test/powershell/engine/ParameterBinding/ParameterBinding.Tests.ps1
//     Describe "Parameter Binding Tests". Ported test/case name:
//
//   - It "Should throw a exception when passing a string that can't be
//     parsed by Int": `test-singleintparameter -Parameter1
//     'exampleInvalidParam'` throws
//     "ParameterArgumentTransformationError,test-singleintparameter" with a
//     message matching both "exampleInvalidParam" and "Parameter1".
//
//   - Source file: test/powershell/Language/Scripting/ParameterBinding.Tests.ps1
//     Describe "Tests for parameter binding". Ported test/case name:
//
//   - It 'Multiple positional parameters case 1': of its five assertions,
//     two carry over to a purely-positional binder (this package has no
//     PowerShell-style `-a value` way to address a positional by name, so
//     the `-a`-prefixed variants are not portable and are skipped):
//     `( get-foo -b d c ) -join ',' | Should -BeExactly 'c,d'` and
//     `( get-foo c -b d ) -join ',' | Should -BeExactly 'c,d'` — a named
//     flag and a bare positional bind the same way regardless of order.
func TestRegisteredCommandSchemaPortedFromPowerShellParameterBinding(t *testing.T) {
	t.Parallel()

	t.Run("int conversion error names the parameter and the bad value", func(t *testing.T) {
		t.Parallel()
		schema := &interp.CommandSchema{
			Flags: []interp.CommandFlag{
				{Name: "Parameter1", Type: "int"},
			},
		}
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "test-singleintparameter" {
				return interp.ResolvedCommand{Desc: "registered command (script)", Schema: schema}, true
			}
			return interp.ResolvedCommand{}, false
		}
		stdout, stderr, calls, err := runRegisteredSchema(t, `test-singleintparameter --Parameter1 exampleInvalidParam; echo after`, resolve)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stdout != "after\n" || calls != 0 {
			t.Fatalf("stdout=%q calls=%d", stdout, calls)
		}
		if !strings.Contains(stderr, "Parameter1") || !strings.Contains(stderr, `"exampleInvalidParam"`) {
			t.Fatalf("stderr = %q, want mentions of Parameter1 and exampleInvalidParam", stderr)
		}
	})

	t.Run("named flag and bare positional bind the same regardless of order", func(t *testing.T) {
		t.Parallel()
		schema := &interp.CommandSchema{
			Flags:       []interp.CommandFlag{{Name: "b", Type: "string"}},
			Positionals: []interp.CommandParameter{{Name: "a", Type: "string", Required: true}},
		}
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "get-foo" {
				return interp.ResolvedCommand{Desc: "registered command (script)", Schema: schema}, true
			}
			return interp.ResolvedCommand{}, false
		}
		for _, src := range []string{
			`get-foo --b d c`,
			`get-foo c --b d`,
		} {
			stdout, stderr, calls, err := runRegisteredSchema(t, src, resolve)
			if err != nil {
				t.Fatalf("%s: Run: %v\nstderr=%s", src, err, stderr)
			}
			if want := "get-foo|--b=d|c\n"; stdout != want || calls != 1 {
				t.Fatalf("%s: stdout=%q calls=%d, want %q calls=1", src, stdout, calls, want)
			}
		}
	})
}

// TestRegisteredCommandSchemaNilSchemaPreservesArgv is focused coverage
// (not an upstream port): ResolvedCommand.Schema == nil must leave argv,
// including flag-shaped tokens the runner never inspects, byte-for-byte
// unchanged before ExecHandler sees them.
func TestRegisteredCommandSchemaNilSchemaPreservesArgv(t *testing.T) {
	t.Parallel()
	resolve := func(name string) (interp.ResolvedCommand, bool) {
		if name == "legacy" {
			return interp.ResolvedCommand{Desc: "legacy is a registered command (script)"}, true
		}
		return interp.ResolvedCommand{}, false
	}
	stdout, stderr, calls, err := runRegisteredSchema(t, `legacy --surprise value --count=nope -x`, resolve)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "legacy|--surprise|value|--count=nope|-x\n"
	if stdout != want || stderr != "" || calls != 1 {
		t.Fatalf("stdout=%q stderr=%q calls=%d, want stdout=%q calls=1", stdout, stderr, calls, want)
	}
}

// TestRegisteredCommandSchemaMalformedSchemaFailsLoudly is focused coverage
// (not an upstream port, though the first case mirrors the spirit of
// PowerShell/PowerShell's "Should throw a parameter binding exception when
// two parameters have the same position" in
// test/powershell/engine/ParameterBinding/ParameterBinding.Tests.ps1,
// Describe "Parameter Binding Tests" — a self-contradictory parameter
// declaration is rejected before the command body ever runs): a schema an
// embedder registers wrong must fail loudly and never reach ExecHandler.
func TestRegisteredCommandSchemaMalformedSchemaFailsLoudly(t *testing.T) {
	t.Parallel()

	t.Run("duplicate flag name", func(t *testing.T) {
		t.Parallel()
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "bad" {
				return interp.ResolvedCommand{
					Desc: "bad is a registered command (script)",
					Schema: &interp.CommandSchema{Flags: []interp.CommandFlag{
						{Name: "mode", Type: "string"},
						{Name: "mode", Type: "string"},
					}},
				}, true
			}
			return interp.ResolvedCommand{}, false
		}
		stdout, stderr, calls, err := runRegisteredSchema(t, `bad --mode fast; echo after`, resolve)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stdout != "after\n" || calls != 0 {
			t.Fatalf("stdout=%q calls=%d", stdout, calls)
		}
		if !strings.Contains(stderr, `invalid registered-command schema for bad: duplicate flag "mode"`) {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("required positional follows an optional one", func(t *testing.T) {
		t.Parallel()
		resolve := func(name string) (interp.ResolvedCommand, bool) {
			if name == "bad2" {
				return interp.ResolvedCommand{
					Desc: "bad2 is a registered command (script)",
					Schema: &interp.CommandSchema{Positionals: []interp.CommandParameter{
						{Name: "first", Type: "string", Default: "x"},
						{Name: "second", Type: "string", Required: true},
					}},
				}, true
			}
			return interp.ResolvedCommand{}, false
		}
		stdout, stderr, calls, err := runRegisteredSchema(t, `bad2 a b; echo after`, resolve)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if stdout != "after\n" || calls != 0 {
			t.Fatalf("stdout=%q calls=%d", stdout, calls)
		}
		if !strings.Contains(stderr, `invalid registered-command schema for bad2: required positional "second" follows an optional positional`) {
			t.Fatalf("stderr = %q", stderr)
		}
	})
}

// TestRegisteredCommandSchemaBindsOncePerInvocationAndBlocksExecHandler is
// focused coverage (not an upstream port) for two properties of the
// bind/validate-input design: (1) a resolver-backed schema is resolved and
// bound exactly once per invocation — a second, independent invocation of
// the same registered command is bound again from scratch, it is never
// skipped by the hash-table fast path a plain exec would use (see the
// `!registeredSchema` guards around r.cmdHashTable in execAs) — and (2)
// invalid input for one invocation never reaches ExecHandler, while a
// prior, valid invocation is unaffected.
func TestRegisteredCommandSchemaBindsOncePerInvocationAndBlocksExecHandler(t *testing.T) {
	t.Parallel()
	schema := &interp.CommandSchema{
		Flags:       []interp.CommandFlag{{Name: "count", Type: "int", Required: true}},
		Positionals: []interp.CommandParameter{{Name: "id", Type: "string", Required: true}},
	}
	var resolveCalls int
	resolve := func(name string) (interp.ResolvedCommand, bool) {
		if name == "work" {
			resolveCalls++
			return interp.ResolvedCommand{Desc: "registered command (script)", Schema: schema}, true
		}
		return interp.ResolvedCommand{}, false
	}
	stdout, stderr, calls, err := runRegisteredSchema(t, `work --count 1 a; work --count bad b; echo after`, resolve)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "work|--count=1|a\nafter\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if calls != 1 {
		t.Fatalf("ExecHandler calls = %d, want 1 (only the first, valid invocation)", calls)
	}
	if !strings.Contains(stderr, `--count expects int, got "bad"`) {
		t.Fatalf("stderr = %q, want the second invocation's conversion error", stderr)
	}
	if resolveCalls != 2 {
		t.Fatalf("resolver calls = %d, want 2 (once per invocation, valid or not)", resolveCalls)
	}
}

func TestRegisteredCommandSchemaIgnoresExistingHash(t *testing.T) {
	resolve := func(name string) (interp.ResolvedCommand, bool) {
		return interp.ResolvedCommand{Schema: &interp.CommandSchema{}}, name == "work"
	}
	out, errs, calls, err := runRegisteredSchema(t, `hash -p /old/external/work work; work`, resolve)
	if err != nil || out != "work\n" || errs != "" || calls != 1 {
		t.Fatalf("out=%q stderr=%q calls=%d err=%v", out, errs, calls, err)
	}
}

func TestBindCommandSchemaFrontDoor(t *testing.T) {
	schema := &interp.CommandSchema{Positionals: []interp.CommandParameter{{Name: "count", Type: "int", Required: true}}}
	if _, err := interp.BindCommandSchema("work", schema, []string{"work", "bad"}); err == nil {
		t.Fatal("invalid input accepted")
	}
	args, err := interp.BindCommandSchema("work", schema, []string{"work", "03"})
	if err != nil || strings.Join(args, "|") != "work|3" {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if _, err := interp.BindCommandSchema("work", schema, nil); err == nil {
		t.Fatal("empty invocation accepted")
	}
}
