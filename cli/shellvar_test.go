package cli

import (
	"runtime"
	"strings"
	"testing"
)

// GNU Bash 5.3 oracle (bash 5.3.15, normal and --posix identical; see
// docs/make-shell-startup-oracle.md):
//
//	SHELL absent   -> bound to the login shell, NOT exported
//	SHELL= (empty) -> kept empty, export attribute preserved
//	SHELL=value    -> kept verbatim, export attribute preserved
//
// The absent-SHELL cell is what broke the Profile B make TPs: a make recipe
// shell referencing $SHELL saw an unset variable under bashy where GNU sh
// binds the login shell.
func TestNewRunnerShellStartupMatrix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SHELL startup default is deliberately not synthesized on windows")
	}
	for _, argv0 := range []string{"sh", "bash"} {
		for _, posixFlag := range []bool{false, true} {
			mode := "normal"
			if posixFlag {
				mode = "posix"
			}
			t.Run(argv0+"/"+mode+"/absent", func(t *testing.T) {
				withStrictPosixEnv(t, argv0, posixFlag)
				unsetTestEnv(t, "SHELL")
				r, err := newRunner()
				if err != nil {
					t.Fatal(err)
				}
				vr := r.Env.Get("SHELL")
				if !vr.IsSet() || vr.String() == "" {
					t.Fatalf("SHELL not bound on startup with no inherited SHELL: %#v", vr)
				}
				if vr.Exported {
					t.Fatalf("synthesized SHELL must not be exported: %#v", vr)
				}
				if want := loginShellPath(); vr.String() != want {
					t.Fatalf("SHELL = %q, want login shell %q", vr.String(), want)
				}
			})
			t.Run(argv0+"/"+mode+"/empty", func(t *testing.T) {
				withStrictPosixEnv(t, argv0, posixFlag)
				t.Setenv("SHELL", "")
				r, err := newRunner()
				if err != nil {
					t.Fatal(err)
				}
				vr := r.Env.Get("SHELL")
				if !vr.IsSet() || vr.String() != "" {
					t.Fatalf("explicitly empty SHELL must be preserved empty: %#v", vr)
				}
				if !vr.Exported {
					t.Fatalf("inherited empty SHELL must keep its export attribute: %#v", vr)
				}
			})
			t.Run(argv0+"/"+mode+"/nonempty", func(t *testing.T) {
				withStrictPosixEnv(t, argv0, posixFlag)
				t.Setenv("SHELL", "/custom/oracle-shell")
				r, err := newRunner()
				if err != nil {
					t.Fatal(err)
				}
				vr := r.Env.Get("SHELL")
				if !vr.IsSet() || vr.String() != "/custom/oracle-shell" {
					t.Fatalf("inherited SHELL must be preserved verbatim: %#v", vr)
				}
				if !vr.Exported {
					t.Fatalf("inherited SHELL must keep its export attribute: %#v", vr)
				}
			})
		}
	}
}

func TestLoginShellFromPasswd(t *testing.T) {
	passwd := strings.NewReader(strings.Join([]string{
		"# comment line",
		"root:*:0:0:System Administrator:/var/root:/bin/sh",
		"tester:*:501:20:Test User:/home/tester:/opt/shells/tsh",
		"noshell:*:502:20:No Shell Field:/home/noshell:",
		"short:*:503",
	}, "\n"))
	if got := loginShellFromPasswd(passwd, 501); got != "/opt/shells/tsh" {
		t.Fatalf("uid 501: got %q, want /opt/shells/tsh", got)
	}
	// bash falls back to /bin/sh when the shell field is empty …
	passwd = strings.NewReader("noshell:*:502:20::/home/noshell:\n")
	if got := loginShellFromPasswd(passwd, 502); got != "/bin/sh" {
		t.Fatalf("empty shell field: got %q, want /bin/sh", got)
	}
	// … when the uid has no passwd entry (directory-service hosts) …
	passwd = strings.NewReader("root:*:0:0::/var/root:/bin/sh\n")
	if got := loginShellFromPasswd(passwd, 501); got != "/bin/sh" {
		t.Fatalf("missing uid: got %q, want /bin/sh", got)
	}
	// … and when a line is malformed.
	passwd = strings.NewReader("short:*:501\n")
	if got := loginShellFromPasswd(passwd, 501); got != "/bin/sh" {
		t.Fatalf("malformed line: got %q, want /bin/sh", got)
	}
}
