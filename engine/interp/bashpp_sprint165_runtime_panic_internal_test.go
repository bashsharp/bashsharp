package interp

import "testing"

// Only the runtime's own messages box as runtime error values.
func TestSprint165RuntimeErrorPayload(t *testing.T) {
	cases := map[string]string{
		"runtime error: index out of range [3] with length 2":              bashPPRuntimeBoundsError,
		"runtime error: slice bounds out of range [:5] with capacity 2":    bashPPRuntimeBoundsError,
		"runtime error: invalid memory address or nil pointer dereference": bashPPRuntimeErrorString,
		"runtime error: comparing uncomparable type []int":                 bashPPRuntimeErrorString,
		"close of nil channel":        bashPPRuntimePlainError,
		"send on closed channel":      bashPPRuntimePlainError,
		"makechan: size out of range": bashPPRuntimePlainError,
	}
	for text, want := range cases {
		iv, ok := bashPPRuntimeErrorPayload(text)
		if !ok || iv == nil || bashPPTypeText(iv.dynamic) != want || bashPPRuntimeErrorText(iv) != text {
			t.Fatalf("%q: got %v %+v, want %s", text, ok, iv, want)
		}
	}
	for _, text := range []string{"", "not the runtime's", "Runtime error: x", "close of channel", "FAIL"} {
		if iv, ok := bashPPRuntimeErrorPayload(text); ok || iv != nil {
			t.Fatalf("%q boxed as a runtime error", text)
		}
	}
}
