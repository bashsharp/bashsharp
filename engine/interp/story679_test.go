package interp

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
)

func TestStory679ShebangParser(t *testing.T) {
	tests := []struct {
		line, interp, arg string
	}{
		{"#! /bin/sh\necho ok", "/bin/sh", ""},
		{"#!/usr/bin/env bash\n", "/usr/bin/env", "bash"},
		{"#!/d/a/bash.exe -x\n", "/d/a/bash.exe", "-x"},
		{"#! /bin/sh\r\n", "/bin/sh", ""},
		{"#!/usr/bin/env python -u  \n", "/usr/bin/env", "python -u"},
	}
	for _, tt := range tests {
		interp, arg, ok := parseShebang([]byte(tt.line))
		if !ok || interp != tt.interp || arg != tt.arg {
			t.Errorf("parseShebang(%q) = %q, %q, %v", tt.line, interp, arg, ok)
		}
	}
	if _, _, ok := parseShebang([]byte("#!\n")); ok {
		t.Error("an empty shebang line names no interpreter")
	}
}

func TestStory679ShebangArgs(t *testing.T) {
	got := shebangArgs(`D:\\bin\\bash.exe`, "-x", `D:\\tmp\\s`, []string{"s", "one", "two"})
	want := []string{`D:\\bin\\bash.exe`, "-x", `D:\\tmp\\s`, "one", "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shebangArgs = %#v; want %#v", got, want)
	}
}

func TestStory679WindowsExecFormatError(t *testing.T) {
	errno := syscall.Errno(193)
	for _, err := range []error{
		errno,
		&os.PathError{Op: "fork/exec", Path: "x", Err: errno},
		&exec.Error{Name: "x", Err: errno},
	} {
		if !isExecFormatErrorWindows(err) {
			t.Errorf("did not recognise %T: %v", err, err)
		}
	}
}

func TestStory679DiagnosticShellName(t *testing.T) {
	for in, want := range map[string]string{"bash.exe": "bash", `D:\\bin\\BASH.EXE`: `D:\\bin\\BASH`, "bash": "bash"} {
		if got := diagnosticShellNameMode(in, true); got != want {
			t.Errorf("diagnosticShellNameMode(%q) = %q; want %q", in, got, want)
		}
	}
	if got := diagnosticShellNameMode("bash.exe", false); got != "bash.exe" {
		t.Errorf("non-Windows name changed to %q", got)
	}
}

func TestStory679PosixErrorText(t *testing.T) {
	tests := map[syscall.Errno]string{
		2: "No such file or directory", 3: "No such file or directory", 123: "No such file or directory",
		5: "Permission denied", 80: "File exists", 183: "File exists", 145: "Directory not empty",
		267: "Not a directory", 193: "Exec format error", 32: "Device or resource busy",
		109: "Broken pipe", 232: "Broken pipe", 206: "File name too long",
	}
	for errno, want := range tests {
		err := &os.PathError{Op: "open", Path: "x", Err: errno}
		if got, ok := posixErrorTextMode(err, true); !ok || got != want {
			t.Errorf("errno %d = %q, %v; want %q", errno, got, ok, want)
		}
	}
	if got, ok := posixErrorTextMode(fmt.Errorf("plain"), true); ok || got != "" {
		t.Fatalf("plain error mapped to %q, %v", got, ok)
	}
}
