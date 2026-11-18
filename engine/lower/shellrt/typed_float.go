package shellrt

import "strconv"

// TypedFloatProjection is the source runtime carrier view of an explicitly
// floating declaration. Untyped literal bindings keep their exact rational
// provenance and never enter this path.
func TypedFloatProjection[T ~float32 | ~float64](value T) string {
	return strconv.FormatFloat(float64(value), 'g', -1, 64)
}
