// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package front

import (
	"strings"
	"testing"
)

func TestLeadingGoPackage(t *testing.T) {
	for src, want := range map[string]bool{
		"package main\n":                       true,
		"// c\n/* c */\npackage main\n":        true,
		"//go:build norun\n\npackage main\n":   true,
		"echo package\n":                       false,
		"packages=1\n":                         false,
		"#!/usr/bin/env bashy\npackage main\n": false,
		"":                                     false,
	} {
		if got := LeadingGoPackage([]byte(src)); got != want {
			t.Errorf("LeadingGoPackage(%q) = %v, want %v", src, got, want)
		}
	}
}

func TestCheckShellInputRefusesWithoutFrontEnd(t *testing.T) {
	previous := ShellCheck
	ShellCheck = nil
	t.Cleanup(func() { ShellCheck = previous })
	err := CheckShellInput(GoSourceInput{Files: []GoSourceFile{{Name: "x.bsh", Data: []byte("echo\n")}}})
	if err == nil || !strings.Contains(err.Error(), "not available in this build") {
		t.Fatalf("err = %v", err)
	}
	if err := CheckShellInput(GoSourceInput{}); err == nil {
		t.Fatal("no input must be refused")
	}
}
