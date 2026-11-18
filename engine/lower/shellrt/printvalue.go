package shellrt

import "reflect"

// NativePrintValue projects a compiler-classified native aggregate into the
// value shape used by typed print/println. Shell word projection is separate.
// The caller must retain imported values in their original native shape.
func NativePrintValue(value any) any { return nativePrintValue(reflect.ValueOf(value)) }
func nativePrintValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return nativePrintValue(value.Elem())
	case reflect.Struct:
		out := map[string]any{}
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			out[typ.Field(i).Name] = nativePrintValue(value.Field(i))
		}
		return out
	case reflect.Array, reflect.Slice:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return []any(nil)
		}
		out := make([]any, value.Len())
		for i := range out {
			out[i] = nativePrintValue(value.Index(i))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return map[any]any(nil)
		}
		out := map[any]any{}
		iterator := value.MapRange()
		for iterator.Next() {
			out[nativePrintValue(iterator.Key())] = nativePrintValue(iterator.Value())
		}
		return out
	case reflect.String:
		return value.String()
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Complex64, reflect.Complex128:
		return value.Complex()
	default:
		if value.CanInterface() {
			return value.Interface()
		}
		return nil
	}
}
