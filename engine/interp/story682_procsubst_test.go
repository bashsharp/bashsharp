// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pathconv"
)

// Sprint 245, story 682 (procsub): the Windows process-substitution pipe is
// created as \\.\pipe\sh-np-<hex> but spelled //./pipe/sh-np-<hex> on the
// command line, so `eval cat <(echo x)` does not lose its backslashes to
// the re-read. The shell recognises both spellings for its own open and
// stat of the path.

func TestStory682ProcSubstPipeNames(t *testing.T) {
	t.Parallel()
	native, shell := windowsProcSubstPipeNames("4a373c478eda0e4b")
	if native != `\\.\pipe\sh-np-4a373c478eda0e4b` {
		t.Errorf("native spelling = %q", native)
	}
	if shell != `//./pipe/sh-np-4a373c478eda0e4b` {
		t.Errorf("shell spelling = %q", shell)
	}
	// The command-line spelling survives being read again as shell input
	// unquoted, which is what eval does with the expanded word.
	// eval re-reads the expanded word as shell input: the //./ spelling comes
	// back unchanged, the native one loses its backslashes to escaping.
	reread := func(spelling string) string {
		// Single quotes keep the first read from touching the word, as the
		// procsub expansion does not: only eval's own re-read applies.
		file, err := syntax.NewParser().Parse(strings.NewReader("eval echo '"+spelling+"'"), "")
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		r, err := New(StdIO(nil, &out, &out))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(out.String(), "\n")
	}
	if got := reread(shell); got != shell {
		t.Errorf("eval re-read of %q = %q", shell, got)
	}
	if got := reread(native); got != `\.pipesh-np-4a373c478eda0e4b` {
		t.Errorf("eval re-read of %q = %q; expected the backslashes to be eaten", native, got)
	}
}

func TestStory682ProcSubstPipePathRecognised(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path    string
		windows bool
		want    bool
	}{
		{`//./pipe/sh-np-4a37`, true, true},
		{`\\.\pipe\sh-np-4a37`, true, true},
		{`//./pipe/sh-np-4a37`, false, false}, // not a Unix spelling
		{`//./pipe/other`, true, false},
		{`//./pipe/sh-np-4a37/x`, true, false},
		{`/tmp/sh-np-4a37`, true, false},
		{`//./pipe/`, true, false},
		{``, true, false},
	}
	for _, c := range cases {
		if got := isProcSubstPipePathMode(c.path, c.windows); got != c.want {
			t.Errorf("isProcSubstPipePathMode(%q, windows=%v) = %v, want %v", c.path, c.windows, got, c.want)
		}
	}
	// The synthetic stat only answers for a pipe the shell is serving; see
	// TestProcSubstPipeStatFollowsTheLiveName.
	procSubstPipeRegister("sh-np-4a37")
	defer procSubstPipeRelease("sh-np-4a37")
	info, ok := procSubstPipeStatMode(`//./pipe/sh-np-4a37`, true)
	if !ok || info == nil {
		t.Fatal("no synthetic stat for the shell spelling")
	}
	if info.Mode()&fs.ModeNamedPipe == 0 || info.Mode()&0o600 != 0o600 || info.IsDir() || info.Name() != "sh-np-4a37" {
		t.Errorf("synthetic stat = %v %q", info.Mode(), info.Name())
	}
	if info, ok := procSubstPipeStatMode(`\\.\pipe\sh-np-4a37`, true); !ok || info == nil || info.Name() != "sh-np-4a37" {
		t.Errorf("native spelling stat = %v, %v", info, ok)
	}
	if _, ok := procSubstPipeStatMode(`//./pipe/sh-np-4a37`, false); ok {
		t.Error("a Unix host has no Windows pipes to stat")
	}
}

// The path must reach CreateFile intact: the mount table passes a leading //
// through, absolute joining leaves it alone, and the share-delete open
// (which would CreateFile it with file-oriented flags) declines it so the
// plain os.OpenFile path is taken.
func TestStory682ProcSubstPipePathPassesThroughPathconv(t *testing.T) {
	t.Parallel()
	const p = `//./pipe/sh-np-4a37`
	m := pathconv.NewMounts(`C:\msys64`, nil, `C:\Temp`)
	if got := pathconv.ToOSMountsMode(m, `C:\work`, p, true); got != p {
		t.Errorf("ToOSMountsMode = %q, want passthrough", got)
	}
	if got := pathconv.ToOSMode(`/c/work`, p, true); got != p {
		t.Errorf("ToOSMode = %q, want passthrough", got)
	}
	if got := shellPathJoinAbsMode(`/c/work`, p, true); got != p {
		t.Errorf("shellPathJoinAbsMode = %q, want passthrough", got)
	}
	if windowsShareDeleteEligible(p) || windowsShareDeleteEligible(`\\.\pipe\sh-np-4a37`) {
		t.Error("a device path must not take the share-delete CreateFile")
	}
}
