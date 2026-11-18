// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !linux && !darwin

package interp

import "io"

func writePipelineOutput(w io.Writer, p []byte) (int, error) {
	return w.Write(p)
}
