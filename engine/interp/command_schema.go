// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"strconv"
	"strings"
)

type boundCommandSchema struct {
	args []string
}

// BindCommandSchema validates and normalizes an invocation for embedders whose
// front door bypasses Runner. args includes the command name at index zero.
// It uses the same binder as shell dispatch; a nil schema preserves argv.
func BindCommandSchema(command string, schema *CommandSchema, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s: missing command name", command)
	}
	bound, err := bindCommandSchema(command, schema, args)
	return bound.args, err
}

func bindCommandSchema(command string, schema *CommandSchema, args []string) (boundCommandSchema, error) {
	if schema == nil {
		return boundCommandSchema{args: append([]string(nil), args...)}, nil
	}
	if err := validateCommandSchema(schema); err != nil {
		return boundCommandSchema{}, fmt.Errorf("invalid registered-command schema for %s: %w", command, err)
	}
	values := args[1:]
	positionals := make([]string, 0, len(schema.Positionals))
	flagValues := make(map[int]string, len(schema.Flags))
	flagSet := make(map[int]bool, len(schema.Flags))
	longFlags := make(map[string]int, len(schema.Flags))
	shortFlags := make(map[string]int, len(schema.Flags))
	for i, flag := range schema.Flags {
		longFlags[flag.Name] = i
		if flag.Shorthand != "" {
			shortFlags[flag.Shorthand] = i
		}
	}
	for i := 0; i < len(values); i++ {
		arg := values[i]
		if arg == "--" {
			positionals = append(positionals, values[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			name, value, hasValue := strings.Cut(arg[2:], "=")
			index, ok := longFlags[name]
			if !ok {
				return boundCommandSchema{}, fmt.Errorf("%s: unknown flag --%s", command, name)
			}
			flag := schema.Flags[index]
			if flagSet[index] {
				return boundCommandSchema{}, fmt.Errorf("%s: flag --%s supplied more than once", command, name)
			}
			if !hasValue {
				if commandSchemaType(flag.Type) == "bool" {
					value = "true"
				} else {
					i++
					if i >= len(values) {
						return boundCommandSchema{}, fmt.Errorf("%s: flag --%s requires a value", command, name)
					}
					value = values[i]
				}
			}
			converted, err := convertCommandSchemaValue(command, "--"+name, flag.Type, value, flag.Enum)
			if err != nil {
				return boundCommandSchema{}, err
			}
			flagValues[index] = converted
			flagSet[index] = true
			continue
		}
		if strings.HasPrefix(arg, "-") && len(arg) > 1 && arg != "-" {
			name, value, hasValue := strings.Cut(arg[1:], "=")
			index, ok := shortFlags[name]
			if !ok {
				return boundCommandSchema{}, fmt.Errorf("%s: unknown flag -%s", command, name)
			}
			flag := schema.Flags[index]
			if flagSet[index] {
				return boundCommandSchema{}, fmt.Errorf("%s: flag --%s supplied more than once", command, flag.Name)
			}
			if !hasValue {
				if commandSchemaType(flag.Type) == "bool" {
					value = "true"
				} else {
					i++
					if i >= len(values) {
						return boundCommandSchema{}, fmt.Errorf("%s: flag -%s requires a value", command, name)
					}
					value = values[i]
				}
			}
			converted, err := convertCommandSchemaValue(command, "-"+name, flag.Type, value, flag.Enum)
			if err != nil {
				return boundCommandSchema{}, err
			}
			flagValues[index] = converted
			flagSet[index] = true
			continue
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) > len(schema.Positionals) {
		return boundCommandSchema{}, fmt.Errorf("%s: too many positional arguments: got %d, want %d", command, len(positionals), len(schema.Positionals))
	}
	bound := []string{args[0]}
	for i, flag := range schema.Flags {
		value, ok := flagValues[i]
		if !ok && flag.Default != "" {
			var err error
			value, err = convertCommandSchemaValue(command, "--"+flag.Name, flag.Type, flag.Default, flag.Enum)
			if err != nil {
				return boundCommandSchema{}, err
			}
			ok = true
		}
		if !ok {
			if flag.Required {
				return boundCommandSchema{}, fmt.Errorf("%s: missing required flag --%s", command, flag.Name)
			}
			continue
		}
		bound = append(bound, "--"+flag.Name+"="+value)
	}
	for i, param := range schema.Positionals {
		value := ""
		ok := i < len(positionals)
		if ok {
			value = positionals[i]
		} else if param.Default != "" {
			value = param.Default
			ok = true
		}
		if !ok {
			if param.Required {
				return boundCommandSchema{}, fmt.Errorf("%s: missing required positional %s", command, param.Name)
			}
			continue
		}
		converted, err := convertCommandSchemaValue(command, param.Name, param.Type, value, param.Enum)
		if err != nil {
			return boundCommandSchema{}, err
		}
		bound = append(bound, converted)
	}
	return boundCommandSchema{args: bound}, nil
}

func validateCommandSchema(schema *CommandSchema) error {
	seenParams := make(map[string]bool, len(schema.Positionals))
	optionalSeen := false
	for i, param := range schema.Positionals {
		if param.Name == "" {
			return fmt.Errorf("positional %d has no name", i)
		}
		if optionalSeen && param.Required {
			return fmt.Errorf("required positional %q follows an optional positional", param.Name)
		}
		if seenParams[param.Name] {
			return fmt.Errorf("duplicate positional %q", param.Name)
		}
		seenParams[param.Name] = true
		if !validCommandSchemaType(param.Type) {
			return fmt.Errorf("positional %q has unsupported type %q", param.Name, param.Type)
		}
		if param.Required && param.Default != "" {
			return fmt.Errorf("positional %q is required and has a default", param.Name)
		}
		if !param.Required {
			optionalSeen = true
		}
	}
	seenLong := make(map[string]bool, len(schema.Flags))
	seenShort := make(map[string]bool, len(schema.Flags))
	for i, flag := range schema.Flags {
		if flag.Name == "" {
			return fmt.Errorf("flag %d has no name", i)
		}
		if strings.HasPrefix(flag.Name, "-") || strings.ContainsAny(flag.Name, "= \t\n") {
			return fmt.Errorf("flag %q has an invalid name", flag.Name)
		}
		if seenLong[flag.Name] {
			return fmt.Errorf("duplicate flag %q", flag.Name)
		}
		seenLong[flag.Name] = true
		if flag.Shorthand != "" {
			if strings.HasPrefix(flag.Shorthand, "-") || strings.ContainsAny(flag.Shorthand, "= \t\n") {
				return fmt.Errorf("flag %q has invalid shorthand %q", flag.Name, flag.Shorthand)
			}
			if seenShort[flag.Shorthand] {
				return fmt.Errorf("duplicate shorthand %q", flag.Shorthand)
			}
			seenShort[flag.Shorthand] = true
		}
		if !validCommandSchemaType(flag.Type) {
			return fmt.Errorf("flag %q has unsupported type %q", flag.Name, flag.Type)
		}
		if flag.Required && flag.Default != "" {
			return fmt.Errorf("flag %q is required and has a default", flag.Name)
		}
	}
	return nil
}

func validCommandSchemaType(typ string) bool {
	switch commandSchemaType(typ) {
	case "string", "int", "float", "bool":
		return true
	default:
		return false
	}
}

func commandSchemaType(typ string) string {
	if typ == "" {
		return "string"
	}
	return typ
}

func convertCommandSchemaValue(command, name, typ, value string, enum []string) (string, error) {
	converted := value
	switch commandSchemaType(typ) {
	case "string":
	case "int":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%s: %s expects int, got %q", command, name, value)
		}
		converted = strconv.FormatInt(n, 10)
	case "float":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "", fmt.Errorf("%s: %s expects float, got %q", command, name, value)
		}
		converted = strconv.FormatFloat(f, 'g', -1, 64)
	case "bool":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("%s: %s expects bool, got %q", command, name, value)
		}
		converted = strconv.FormatBool(b)
	default:
		return "", fmt.Errorf("%s: %s has unsupported type %q", command, name, typ)
	}
	if len(enum) > 0 {
		for _, allowed := range enum {
			if converted == allowed {
				return converted, nil
			}
		}
		return "", fmt.Errorf("%s: %s must be one of %s, got %q", command, name, strings.Join(enum, ", "), converted)
	}
	return converted, nil
}
