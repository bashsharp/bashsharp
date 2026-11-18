// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build !windows

package cli

func shellStartupEnv(env []string) []string { return env }
