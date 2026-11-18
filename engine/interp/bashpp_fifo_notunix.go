// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build !unix

package interp

import (
	"context"
	"os"
)

func bashPPFIFOIdentify(ctx context.Context, dirFile *os.File, path string) (bashPPFIFOIdentity, bool, error) {
	return bashPPFIFOIdentity{}, false, nil
}

func bashPPFIFOFileIdentity(file *os.File) (bashPPFIFOIdentity, bool) {
	return bashPPFIFOIdentity{}, false
}

func bashPPFIFOAcquire(ctx context.Context, dirFile *os.File, dir, path string, flags int, key bashPPFIFOIdentity) (*os.File, *os.File, error) {
	return nil, nil, nil
}
