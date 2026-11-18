// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build windows

package cli

import "os"

// NormalizeHostEnv makes os.UserHomeDir/os.TempDir see native spellings; the
// shell re-derives its POSIX $HOME via shellStartupEnv.
func NormalizeHostEnv() { normalizeHostEnv(os.Getenv, os.Setenv) }
