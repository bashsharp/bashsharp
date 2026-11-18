package interp

import (
	"errors"
	"syscall"
)

func posixErrorTextMode(err error, windows bool) (string, bool) {
	if !windows {
		return "", false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return "", false
	}
	switch uintptr(errno) {
	case 2, 3, 123:
		return "No such file or directory", true
	case 5:
		return "Permission denied", true
	case 80, 183:
		return "File exists", true
	case 145:
		return "Directory not empty", true
	case 267:
		return "Not a directory", true
	case 193:
		return "Exec format error", true
	case 32:
		return "Device or resource busy", true
	case 109, 232:
		return "Broken pipe", true
	case 206:
		return "File name too long", true
	default:
		return "", false
	}
}
