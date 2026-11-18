// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build !windows

package cli

// NormalizeHostEnv is a no-op off Windows.
func NormalizeHostEnv() {}
