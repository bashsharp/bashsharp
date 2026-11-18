//go:build !unix && !windows

package interp

func isPlatformBrokenPipeWriteErr(error) bool {
	return false
}
