//go:build !windows

package interp

func posixErrorText(error) (string, bool) { return "", false }
