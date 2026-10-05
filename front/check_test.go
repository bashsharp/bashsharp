// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package front

import (
	"io"
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

type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("stdin was read")
	return 0, io.EOF
}

func TestCollectCheckInput(t *testing.T) {
	for _, operand := range []string{"", "-"} {
		in, err := CollectCheckInput(operand, "", false, strings.NewReader("echo piped\n"))
		if err != nil || len(in.Files) != 1 || string(in.Files[0].Data) != "echo piped\n" || in.Files[0].Name != "-" {
			t.Errorf("operand %q: %+v, %v", operand, in, err)
		}
	}
	in, err := CollectCheckInput("", "", true, failReader{t})
	if err != nil || len(in.Files) != 1 || len(in.Files[0].Data) != 0 || in.Files[0].Name != "-c" {
		t.Errorf("empty -c: %+v, %v", in, err)
	}
	in, err = CollectCheckInput("", "echo x", true, failReader{t})
	if err != nil || string(in.Files[0].Data) != "echo x" {
		t.Errorf("-c: %+v, %v", in, err)
	}
}
