package e2e

import (
	"fmt"
	"reflect"
)

// FormatVal dereferences a pointer value for readable logging output.
// Returns the dereferenced value as a string, or "<nil>" for nil pointers.
func FormatVal(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return "<nil>"
		}
		return fmt.Sprintf("%v", rv.Elem().Interface())
	}
	return fmt.Sprintf("%v", v)
}
