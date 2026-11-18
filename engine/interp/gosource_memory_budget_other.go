//go:build !linux && !darwin

package interp

func goSourceHostMemory() int64 { return 0 }
