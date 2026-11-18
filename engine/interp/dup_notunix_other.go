// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "os"

// dupPipeFd is a no-op on platforms without a handle/fd duplication
// primitive; the original pipe fd is returned. Pipelines will still run, but
// EOF/SIGPIPE propagation is best-effort because the parent retains the
// original fd reference.
func dupPipeFd(f *os.File) (*os.File, bool, error) {
	return f, false, nil
}
