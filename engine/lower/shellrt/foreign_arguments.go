package shellrt

import (
	"fmt"
	"strconv"
)

// ForeignScalarArgument preserves the interpreted fence's scalar text boundary.
// The closed type switch deliberately never invokes String, Error, marshaling,
// or other methods on a caller-supplied object.
func ForeignScalarArgument(value any, kind string) (any, error) {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case int:
		if kind == "int" {
			return v, nil
		}
		text = strconv.FormatInt(int64(v), 10)
	case int64:
		if kind == "int" {
			return v, nil
		}
		text = strconv.FormatInt(v, 10)
	case float64:
		if kind == "float64" {
			return v, nil
		}
		text = strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if kind == "bool" {
			return v, nil
		}
		text = strconv.FormatBool(v)
	default:
		return nil, fmt.Errorf("want %s scalar, got %T", kind, value)
	}
	switch kind {
	case "int":
		return strconv.ParseInt(text, 0, 64)
	case "float64":
		return strconv.ParseFloat(text, 64)
	case "bool":
		return strconv.ParseBool(text)
	}
	return nil, fmt.Errorf("unsupported foreign scalar kind %q", kind)
}
