//go:build windows

package interp

func posixErrorText(err error) (string, bool) { return posixErrorTextMode(err, true) }
