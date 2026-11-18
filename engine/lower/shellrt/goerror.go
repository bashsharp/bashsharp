package shellrt

import "fmt"

// GoErrorValue is the typed error value minted by @go.error().
type GoErrorValue string

func (e GoErrorValue) Error() string { return string(e) }

// GoError adapts a completed Bash++ call status into Go's trailing error
// convention without changing the program status. The status is normalized to
// the 8-bit range a shell `$?` reports, so the error's presence and message
// agree with `$?` and with the interpreter, which truncates the same value.
func GoError(name string, status int) error {
	status = NormalStatus(status)
	if status == 0 {
		return nil
	}
	return GoErrorValue(fmt.Sprintf("%s: exit status %d", name, status))
}
