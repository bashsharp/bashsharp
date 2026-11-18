package shellrt

import "strings"

func Positional(params []string, index int) string { return PositionalDefault(params, index, "") }
func PositionalDefault(params []string, index int, fallback string) string {
	if index < 1 || index > len(params) {
		return fallback
	}
	return params[index-1]
}
func JoinPositionals(params []string) string { return strings.Join(params, " ") }
func PositionalArguments(params []string) []any {
	values := make([]any, len(params))
	for i, value := range params {
		values[i] = value
	}
	return values
}
func StringArguments(values []any) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = Word(value)
	}
	return result
}
