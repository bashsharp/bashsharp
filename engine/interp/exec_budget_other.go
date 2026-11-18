// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "errors"

func execBudgetErr() error { return errors.New("argument list too long") }
