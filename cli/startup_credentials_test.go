// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestNormalizeStartupCredentials(t *testing.T) {
	tests := []struct {
		name       string
		state      startupCredentialState
		privileged bool
		wantCalls  []string
	}{
		{
			name:      "ordinary invocation",
			state:     startupCredentialState{realUID: 1000, effectiveUID: 1000, realGID: 1000, effectiveGID: 1000},
			wantCalls: nil,
		},
		{
			name:      "set-id session drops to caller",
			state:     startupCredentialState{realUID: 1000, effectiveUID: 0, realGID: 1000, effectiveGID: 0},
			wantCalls: []string{"gid:1000", "uid:1000"},
		},
		{
			name:       "privileged mode retains effective identity",
			state:      startupCredentialState{realUID: 1000, effectiveUID: 0, realGID: 1000, effectiveGID: 0},
			privileged: true,
			wantCalls:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			err := normalizeStartupCredentials(tc.state, tc.privileged,
				func(id int) error { calls = append(calls, "gid:"+strconv.Itoa(id)); return nil },
				func(id int) error { calls = append(calls, "uid:"+strconv.Itoa(id)); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("credential calls = %v, want %v", calls, tc.wantCalls)
			}
		})
	}
}

func TestNormalizeStartupCredentialsFailsClosed(t *testing.T) {
	want := errors.New("setgid denied")
	uidCalled := false
	err := normalizeStartupCredentials(
		startupCredentialState{realUID: 1000, effectiveUID: 0, realGID: 1000, effectiveGID: 0},
		false,
		func(int) error { return want },
		func(int) error { uidCalled = true; return nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if uidCalled {
		t.Fatal("setuid called after setgid failure")
	}
}

func TestCommandLinePrivilegedMode(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"bash"}, false},
		{[]string{"bash", "-o", "privileged"}, true},
		{[]string{"bash", "-o", "privileged", "-bashy-plus-o", "privileged"}, false},
		{[]string{"bash", "-bashy-plus-o", "privileged", "-o", "privileged"}, true},
		{[]string{"bash", "script", "-o", "privileged"}, false},
	} {
		os.Args = tc.args
		if got := commandLinePrivilegedMode(); got != tc.want {
			t.Errorf("commandLinePrivilegedMode(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestSecureStartupEnv(t *testing.T) {
	old := startupPrivilegedMode
	t.Cleanup(func() { startupPrivilegedMode = old })
	startupPrivilegedMode = true
	got := secureStartupEnv([]string{
		"PATH=/bin",
		"SHELLOPTS=posix:xtrace",
		"BASHOPTS=extglob",
		"CDPATH=/private",
		"GLOBIGNORE=*",
		"BASH_FUNC_probe%%=() { echo unsafe; }",
		"KEEP=value",
	})
	want := []string{"PATH=/bin", "KEEP=value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secureStartupEnv() = %q, want %q", got, want)
	}
}
