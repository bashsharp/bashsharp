// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package cli

import "testing"

func TestNormalizeHostEnv(t *testing.T) {
	env := map[string]string{"USERPROFILE": "/c/Users/x", "HOME": "/c/Users/x", "TMP": `C:\t`, "TEMP": "/d", "OTHER": "/c/z"}
	normalizeHostEnv(func(k string) string { return env[k] }, func(k, v string) error { env[k] = v; return nil })
	want := map[string]string{"USERPROFILE": `C:\Users\x`, "HOME": `C:\Users\x`, "TMP": `C:\t`, "TEMP": `D:\`, "OTHER": "/c/z"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if got := posixDriveToNative("/tmp"); got != "/tmp" {
		t.Errorf("/tmp changed: %q", got)
	}
}
