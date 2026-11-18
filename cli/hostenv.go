// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package cli

import "strings"

// posixDriveToNative converts "/c/Users/x" to `C:\Users\x`; any other value is returned unchanged.
func posixDriveToNative(v string) string {
	if len(v) < 2 || v[0] != '/' || !asciiLetter(v[1]) || (len(v) > 2 && v[2] != '/') {
		return v
	}
	rest := strings.ReplaceAll(v[2:], "/", `\`)
	if rest == "" {
		rest = `\`
	}
	return strings.ToUpper(v[1:2]) + ":" + rest
}

func asciiLetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// normalizeHostEnv rewrites the host home/temp variables to native spellings.
func normalizeHostEnv(getenv func(string) string, setenv func(k, v string) error) {
	for _, k := range []string{"USERPROFILE", "HOME", "TMP", "TEMP"} {
		v := getenv(k)
		if n := posixDriveToNative(v); n != v {
			_ = setenv(k, n)
		}
	}
}
