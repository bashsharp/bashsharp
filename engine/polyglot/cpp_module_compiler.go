package polyglot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// cppModuleCompiler is deliberately pinned: cc1 is an internal interface.
// All settings come from the same normal Zig object driver used by consumers;
// only the action and output change. Never replay an executable from a trace.
func cppModuleCompiler(ctx context.Context, compiler string, environment *EnvironmentPlan) error {
	if environment == nil || len(environment.ExecutableArgs) != 1 || environment.ExecutableArgs[0] != "c++" {
		return fmt.Errorf("C++ module assets require the pinned Zig 0.16.0 c++ provider")
	}
	cmd := exec.CommandContext(ctx, compiler, "version")
	configureNativeCompiler(cmd, environment)
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != "0.16.0" {
		return fmt.Errorf("C++ module assets require Zig 0.16.0")
	}
	return nil
}

// quotedCompilerArgv parses the driver's quoted argv as data, not shell code.
func quotedCompilerArgv(line string) ([]string, error) {
	var args []string
	for line = strings.TrimSpace(line); line != ""; line = strings.TrimSpace(line) {
		if line[0] != '"' {
			return nil, fmt.Errorf("unquoted compiler trace argument")
		}
		quoted, err := strconv.QuotedPrefix(line)
		if err != nil {
			return nil, err
		}
		arg, err := strconv.Unquote(quoted)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		line = line[len(quoted):]
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			return nil, fmt.Errorf("invalid compiler trace separator")
		}
	}
	return args, nil
}

func moduleBMIArgs(trace, compiler, source, object, bmi string) ([]string, error) {
	var args []string
	for _, line := range strings.Split(trace, "\n") {
		if !strings.Contains(line, `"-cc1"`) {
			continue
		}
		if args != nil {
			return nil, fmt.Errorf("C++ module driver emitted multiple cc1 commands")
		}
		parsed, err := quotedCompilerArgv(line)
		if err != nil {
			return nil, fmt.Errorf("C++ module driver trace: %w", err)
		}
		if len(parsed) < 3 || parsed[1] != "-cc1" {
			return nil, fmt.Errorf("invalid C++ module cc1 command")
		}
		expected, err := os.Stat(compiler)
		if err != nil {
			return nil, err
		}
		actual, err := os.Stat(parsed[0])
		if err != nil || !os.SameFile(expected, actual) {
			return nil, fmt.Errorf("C++ module trace executable differs from selected compiler")
		}
		args = append([]string(nil), parsed[1:]...)
	}
	if args == nil {
		return nil, fmt.Errorf("C++ module driver emitted no cc1 command")
	}
	actions, outputs, sources := 0, 0, 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-emit-obj":
			args[i] = "-emit-module-interface"
			actions++
		case "-o":
			if i+1 >= len(args) || args[i+1] != object {
				return nil, fmt.Errorf("unexpected C++ module driver output")
			}
			args[i+1] = bmi
			outputs++
			i++
		default:
			if args[i] == source {
				sources++
			}
			if args[i] == "-disable-module-hash" || args[i] == "-fno-validate-pch" || strings.HasPrefix(args[i], "-fmodule-file-cache-key") {
				return nil, fmt.Errorf("unsupported C++ module validation settings")
			}
		}
	}
	if actions != 1 || outputs != 1 || sources != 1 || args[len(args)-1] != source {
		return nil, fmt.Errorf("unexpected C++ module cc1 action, source or output")
	}
	return args, nil
}

func moduleInfoMatches(output string, unit NativeModuleUnit) error {
	name := ""
	kind := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Module name: ") {
			name = strings.TrimPrefix(line, "Module name: ")
		}
		if strings.Contains(line, " is the Primary Module at index #") {
			switch {
			case strings.HasPrefix(line, "Interface Unit '"), strings.HasPrefix(line, "Partition Interface '"):
				kind = "interface"
			case strings.HasPrefix(line, "Partition Implementation '"):
				kind = "internal-partition"
			}
		}
	}
	if name != unit.Name || kind != unit.Kind {
		return fmt.Errorf("C++ module declaration mismatch: manifest %s %q, compiler %s %q", unit.Kind, unit.Name, kind, name)
	}
	return nil
}

func buildNativeModules(ctx context.Context, compiler string, environment *EnvironmentPlan, dir string, hostIncludes []string) ([]string, []string, error) {
	if environment == nil || len(environment.NativeModules) == 0 {
		return nil, nil, nil
	}
	if err := cppModuleCompiler(ctx, compiler, environment); err != nil {
		return nil, nil, err
	}
	run := func(args []string) (string, string, error) {
		cmd := exec.CommandContext(ctx, compiler, args...)
		configureNativeCompiler(cmd, environment)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			return stdout.String(), stderr.String(), nativeCompilerError("cpp", err, stderr.String())
		}
		return stdout.String(), stderr.String(), nil
	}
	providers := map[string]NativeModuleUnit{}
	for _, unit := range environment.NativeModules {
		if unit.Kind != "implementation" {
			providers[unit.Name] = unit
		}
	}
	bmis := map[string]string{}
	var allMappings, objects []string
	for i, unit := range environment.NativeModules {
		prefix := filepath.Join(dir, fmt.Sprintf("asset-%03d", i))
		object := prefix + ".o"
		var mappings []string
		seen := map[string]bool{}
		var dependency func(string) error
		dependency = func(name string) error {
			if seen[name] {
				return nil
			}
			seen[name] = true
			bmi, ok := bmis[name]
			if !ok {
				return fmt.Errorf("C++ module %q dependency %q was not built", unit.Name, name)
			}
			for _, dep := range providers[name].Requires {
				if err := dependency(dep); err != nil {
					return err
				}
			}
			mappings = append(mappings, "-fmodule-file="+name+"="+bmi)
			return nil
		}
		for _, dep := range unit.Requires {
			if err := dependency(dep); err != nil {
				return nil, nil, err
			}
		}
		args := append(leadingArgs(environment), "-std=c++20", "-O0")
		args = append(args, nativeIncludeArgs(environment)...)
		args = append(args, hostIncludes...)
		args = append(args, mappings...)
		// The compiler checks the declared name even for non-importable
		// implementation units, which have no standalone BMI to inspect.
		args = append(args, "-Xclang", "-fmodule-name="+unit.Name, "-c", "-x", "c++", "-o", object, unit.Source)
		if unit.Kind != "implementation" {
			traceArgs := append([]string(nil), args...)
			traceArgs = append(traceArgs, "-###")
			_, trace, err := run(traceArgs)
			if err != nil {
				return nil, nil, err
			}
			bmi := prefix + ".pcm"
			bmiArgs, err := moduleBMIArgs(trace, compiler, unit.Source, object, bmi)
			if err != nil {
				return nil, nil, err
			}
			if _, _, err := run(bmiArgs); err != nil {
				return nil, nil, err
			}
			infoArgs := append([]string{"-cc1"}, mappings...)
			infoArgs = append(infoArgs, "-module-file-info", bmi)
			info, _, err := run(infoArgs)
			if err != nil {
				return nil, nil, err
			}
			if err := moduleInfoMatches(info, unit); err != nil {
				return nil, nil, err
			}
			bmis[unit.Name] = bmi
			allMappings = append(allMappings, "-fmodule-file="+unit.Name+"="+bmi)
		}
		if _, _, err := run(args); err != nil {
			return nil, nil, err
		}
		objects = append(objects, object)
	}
	return allMappings, objects, nil
}
