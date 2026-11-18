// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "fmt"

func (r *Runner) newProcSubstPipe(substWrites bool) (procSubstPipe, error) {
	return nil, fmt.Errorf("process substitution is unsupported on this platform")
}
