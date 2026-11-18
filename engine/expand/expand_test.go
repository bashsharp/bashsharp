// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func parseWord(t *testing.T, src string) *syntax.Word {
	t.Helper()
	p := syntax.NewParser()
	word, err := p.Document(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return word
}

func TestGlobSortUsesShellStatAndAccessTime(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("access time provider unavailable on this target")
	}
	dir := t.TempDir()
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, file := range []struct {
		name        string
		access, mod time.Time
	}{
		{"a", base.Add(2 * time.Hour), base},
		{"z", base, base.Add(2 * time.Hour)},
	} {
		path := filepath.Join(dir, file.name)
		var contents []byte
		if file.name == "z" {
			contents = []byte("longer")
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, file.access, file.mod); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		key  string
		want string
	}{
		{"+atime", "z,a"},
		{"+mtime", "a,z"},
		{"-size", "z,a"},
	} {
		cfg := &Config{Env: ListEnviron("GLOBSORT=" + tc.key)}
		cfg.Stat = func(p string) (fs.FileInfo, error) {
			if !strings.HasPrefix(p, "/shell/") {
				t.Fatalf("stat path %q did not retain shell spelling", p)
			}
			return os.Stat(filepath.Join(dir, strings.TrimPrefix(p, "/shell/")))
		}
		matches := []string{"a", "z"}
		cfg.sortGlobMatches("/shell", matches)
		if got := strings.Join(matches, ","); got != tc.want {
			t.Errorf("GLOBSORT=%s: got %q, want %q", tc.key, got, tc.want)
		}
	}
}

func parseCallArg(t *testing.T, src string, index int) *syntax.Word {
	t.Helper()
	p := syntax.NewParser()
	file, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("wanted call expression, got %T", file.Stmts[0].Cmd)
	}
	return call.Args[index]
}

func parseCallArgBashPosix(t *testing.T, src string, index int) *syntax.Word {
	t.Helper()
	p := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.PosixMode(true))
	file, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("wanted call expression, got %T", file.Stmts[0].Cmd)
	}
	return call.Args[index]
}

func TestConfigNils(t *testing.T) {
	os.Setenv("EXPAND_GLOBAL", "value")
	tests := []struct {
		name string
		cfg  *Config
		src  string
		want string
	}{
		{
			"NilConfig",
			nil,
			"$EXPAND_GLOBAL",
			"",
		},
		{
			"ZeroConfig",
			&Config{},
			"$EXPAND_GLOBAL",
			"",
		},
		{
			"EnvConfig",
			&Config{Env: ListEnviron(os.Environ()...)},
			"$EXPAND_GLOBAL",
			"value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			word := parseWord(t, tc.src)
			got, err := Literal(tc.cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestFormatLocaleDecimal(t *testing.T) {
	got, _, err := Format(&Config{Env: ListEnviron("LANG=de_DE.UTF-8")}, "%.4f", []string{"1"})
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if want := "1,0000"; got != want {
		t.Fatalf("wanted %q, got %q", want, got)
	}

	got, _, err = Format(&Config{Env: ListEnviron("LANG=C")}, "%.4f", []string{"1"})
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if want := "1.0000"; got != want {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestFormatLocaleUnicodeEscapes(t *testing.T) {
	tests := []struct {
		env  string
		fmt  string
		want string
	}{
		{"LC_CTYPE=fr_FR.ISO8859-1", `\U000000e9`, "\xe9"},
		{"LC_CTYPE=zh_TW.BIG5", `\U000003a8`, "\xa3Z"},
		{"LC_CTYPE=ja_JP.SJIS", `\U0000ff8e`, "\xce"},
		{"LC_CTYPE=C.UTF-8", `\U000000e9`, "é"},
		{"LC_CTYPE=en_US.UTF-8", `\U01000000`, "\xf9\x80\x80\x80\x80"},
		{"LC_CTYPE=en_US.UTF-8", `\U70000000`, "\xfd\xb0\x80\x80\x80\x80"},
		{"LC_CTYPE=en_US.UTF-8", `\Uffffffff`, ""},
	}
	for _, tc := range tests {
		got, _, err := Format(&Config{Env: ListEnviron(tc.env)}, tc.fmt, nil)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.env, err)
		}
		if got != tc.want {
			t.Fatalf("%s: wanted bytes %v, got %v", tc.env, []byte(tc.want), []byte(got))
		}
	}
}

func TestParamRemovePatternInvalidByte(t *testing.T) {
	cfg := &Config{Env: ListEnviron(
		"euro=\342\202\254",
		"mid=\202",
	)}
	word := parseWord(t, `${euro##*$mid}`)
	got, err := Literal(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if want := "\254"; got != want {
		t.Fatalf("wanted bytes %v, got %v", []byte(want), []byte(got))
	}
}

// TestParamRemovePatternLocaleClass checks that ${var#[[:upper:]]}-style
// POSIX-class prefix/suffix removal honors a declared non-C locale (both
// UTF-8 and single-byte ISO-8859-1 text), while still leaving the C/POSIX
// and unset defaults ASCII-only. This is a pure Go string comparison against
// hardcoded locale tables, not a call into the host's C library, so no
// locale-availability skip is needed.
func TestParamRemovePatternLocaleClass(t *testing.T) {
	tests := []struct {
		name string
		env  string
		src  string
		want string
	}{
		{"utf8 upper prefix stripped", "LC_ALL=de_DE.UTF-8", `${x#[[:upper:]]}`, "test"},
		{"c locale upper prefix kept", "LC_ALL=C", `${x#[[:upper:]]}`, "Étest"},
		{"unset locale upper prefix kept", "", `${x#[[:upper:]]}`, "Étest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{`x=Étest`}
			if tc.env != "" {
				env = append(env, tc.env)
			}
			cfg := &Config{Env: ListEnviron(env...)}
			word := parseWord(t, tc.src)
			got, err := Literal(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}

	// ISO-8859-1 stores É as the single raw byte 0xC9, not valid UTF-8 on
	// its own; the locale-aware matcher must still recognize it as upper.
	cfg := &Config{Env: ListEnviron("x=\xc9test", "LC_ALL=de_DE.ISO-8859-1")}
	word := parseWord(t, `${x#[[:upper:]]}`)
	got, err := Literal(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if want := "test"; got != want {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestDocumentParamExpDefaultQuoteRemoval(t *testing.T) {
	cfg := &Config{Env: ListEnviron("P=A")}
	word := parseWord(t, `${P+\"$P\"}`)
	got, err := Document(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if want := `"A"`; got != want {
		t.Fatalf("wanted %q, got %q", want, got)
	}

	word = parseWord(t, `${P+$'A\n'}`)
	got, err = Document(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if want := `$'A\n'`; got != want {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestFieldsIdempotency(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{
			"{1..4}",
			[]string{"1", "2", "3", "4"},
		},
		{
			"a{1..4}",
			[]string{"a1", "a2", "a3", "a4"},
		},
	}
	for _, tc := range tests {
		word := parseWord(t, tc.src)
		for range 2 {
			got, err := Fields(nil, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		}
	}
}

func TestFieldsLegacyBacktickAdjacentLiteral(t *testing.T) {
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := p.Parse(strings.NewReader("got=`capture echo \\\\$\\`expr 1 + 1\\``\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	outerCall := file.Stmts[0].Cmd.(*syntax.CallExpr)
	outerSub := outerCall.Assigns[0].Value.Parts[0].(*syntax.CmdSubst)
	capture := outerSub.Stmts[0].Cmd.(*syntax.CallExpr)
	word := capture.Args[2]
	cfg := &Config{CmdSubst: func(w io.Writer, _ *syntax.CmdSubst) error {
		_, err := io.WriteString(w, "2\n")
		return err
	}}
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"$2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields() = %q, want %q (args=%d parts=%d)", got, want, len(capture.Args), len(word.Parts))
	}
}

func TestFieldsMultibyteIFS(t *testing.T) {
	cfg := &Config{Env: ListEnviron("a=AéB", "IFS=é")}
	word := parseWord(t, `$a`)

	type result struct {
		fields []string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		got, err := Fields(cfg, word)
		done <- result{got, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("did not want error, got %v", got.err)
		}
		if want := []string{"A", "B"}; !reflect.DeepEqual(got.fields, want) {
			t.Fatalf("wanted %q, got %q", want, got.fields)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("field splitting with multibyte IFS did not terminate")
	}
}

func TestFieldsParamExpSubstWordQuotes(t *testing.T) {
	cfg := &Config{Env: ListEnviron("A=aaa bbb ccc")}
	tests := []struct {
		src  string
		want []string
	}{
		{`${B:-"$A"}`, []string{"aaa bbb ccc"}},
		{`${B-"$A"}`, []string{"aaa bbb ccc"}},
		{`${B:-""}`, []string{""}},
	}
	for _, tc := range tests {
		word := parseWord(t, tc.src)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsParamExpSubstWordQuotedAt(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"IFS": {Set: true, Kind: String, Str: " \t\n"},
		"1":   {Set: true, Kind: String, Str: "a b"},
		"@":   {Set: true, Kind: Indexed, List: []string{"a b", "c", "d"}},
		"*":   {Set: true, Kind: Indexed, List: []string{"a b", "c", "d"}},
	}}
	tests := []struct {
		src  string
		want []string
	}{
		{`${1+"$@"}`, []string{"a b", "c", "d"}},
		{`${foo:-"$@"}`, []string{"a b", "c", "d"}},
		{`${foo-"x$@y"}`, []string{"xa b", "c", "dy"}},
		{`${foo:-"x$@y"}`, []string{"xa b", "c", "dy"}},
		{`${1+"x$@y"}`, []string{"xa b", "c", "dy"}},
		{`${1:+"x$@y"}`, []string{"xa b", "c", "dy"}},
		{`${foo-"x$*y"}`, []string{"xa b c dy"}},
		{`echo "${1+  $@  }"`, []string{"  a b", "c", "d  "}},
	}
	for _, tc := range tests {
		word := parseWord(t, tc.src)
		if strings.HasPrefix(tc.src, "echo ") {
			word = parseCallArg(t, tc.src, 1)
		}
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsParamExpSubstWordUnquotedAt(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"IFS": {Set: true, Kind: String, Str: " \t\n"},
		"@":   {Set: true, Kind: Indexed, List: []string{"a", "bb", "c"}},
	}}
	word := parseWord(t, `${foo-x$@y}`)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"xa", "bb", "cy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestFieldsParamExpUnsetPositionalDefault(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"*": {Kind: Indexed},
		"@": {Kind: Indexed},
	}}
	for _, src := range []string{`${*-x}`, `${@-x}`} {
		word := parseWord(t, src)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", src, err)
		}
		want := []string{"x"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: wanted %q, got %q", src, want, got)
		}
	}
	for _, src := range []string{`echo "${*-x}"`, `echo "${@-x}"`} {
		word := parseCallArg(t, src, 1)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", src, err)
		}
		want := []string{"x"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: wanted %q, got %q", src, want, got)
		}
	}
}

func TestFieldsQuotedAtDefaultPreservesElements(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"@": {Set: true, Kind: Indexed, List: []string{"a", "b"}},
	}}
	word := parseCallArg(t, `echo "${@-}"`, 1)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestFieldsUnquotedStarNullIFSPreservesElements(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"IFS": {Set: true, Kind: String, Str: ""},
		"*":   {Set: true, Kind: Indexed, List: []string{"bob", "tom dick harry", "joe"}},
		"a":   {Set: true, Kind: Indexed, List: []string{"bob", "tom dick harry", "joe"}},
	}}
	tests := []struct {
		src  string
		want []string
	}{
		{`echo $*`, []string{"bob", "tom dick harry", "joe"}},
		{`echo ${a[*]}`, []string{"bob", "tom dick harry", "joe"}},
		{`echo x${a[*]}y`, []string{"xbob", "tom dick harry", "joey"}},
	}
	for _, tc := range tests {
		word := parseCallArg(t, tc.src, 1)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsBash53WordSplitSpecialParams(t *testing.T) {
	tests := []struct {
		name string
		env  testEnv
		src  string
		want []string
	}{
		{
			name: "null IFS prefix at",
			env: testEnv{
				"IFS":           {Set: true, Kind: String, Str: ""},
				"gLwbmGzS_var1": {Set: true, Kind: String, Str: "1"},
				"gLwbmGzS_var2": {Set: true, Kind: String, Str: "2"},
			},
			src:  `${!gLwbmGzS_@}`,
			want: []string{"gLwbmGzS_var1", "gLwbmGzS_var2"},
		},
		{
			name: "null IFS prefix star",
			env: testEnv{
				"IFS":           {Set: true, Kind: String, Str: ""},
				"gLwbmGzS_var1": {Set: true, Kind: String, Str: "1"},
				"gLwbmGzS_var2": {Set: true, Kind: String, Str: "2"},
			},
			src:  `${!gLwbmGzS_*}`,
			want: []string{"gLwbmGzS_var1gLwbmGzS_var2"},
		},
		{
			name: "null IFS array keys at",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"a":   {Set: true, Kind: Indexed, List: []string{"v1", "v2", "v3"}},
			},
			src:  `${!a[@]}`,
			want: []string{"0", "1", "2"},
		},
		{
			name: "null IFS array keys star",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"a":   {Set: true, Kind: Indexed, List: []string{"v1", "v2", "v3"}},
			},
			src:  `${!a[*]}`,
			want: []string{"0 1 2"},
		},
		{
			name: "null IFS star drops trailing empty",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"*":   {Set: true, Kind: Indexed, List: []string{"a b", "c", ""}},
			},
			src:  `echo $*`,
			want: []string{"a b", "c"},
		},
		{
			name: "custom IFS at preserves middle empty",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: "zx"},
				"@":   {Set: true, Kind: Indexed, List: []string{"one", "", "two"}},
			},
			src:  `echo $@`,
			want: []string{"one", "", "two"},
		},
		{
			// Unquoted $* behaves like $@: a non-whitespace IFS keeps
			// the empty positional parameter as its own field rather
			// than IFS-joining the elements into one string.
			name: "custom IFS star preserves middle empty",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: "zx"},
				"*":   {Set: true, Kind: Indexed, List: []string{"one", "", "two"}},
			},
			src:  `echo $*`,
			want: []string{"one", "", "two"},
		},
		{
			// Null IFS drops the empty parameters but keeps the word
			// boundaries between them, so the `=` prefix and suffix do
			// not glue into a single `==` field (bash 5.3).
			name: "null IFS at empties drop keep boundaries",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"@":   {Set: true, Kind: Indexed, List: []string{"", "", "", "", ""}},
			},
			src:  `echo =$@=`,
			want: []string{"=", "="},
		},
		{
			name: "null IFS star empties drop keep boundaries",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"*":   {Set: true, Kind: Indexed, List: []string{"", "", "", "", ""}},
			},
			src:  `echo =$*=`,
			want: []string{"=", "="},
		},
		{
			// A non-whitespace IFS keeps each empty parameter as its
			// own field: five empties with `=` glued onto the first and
			// last give five fields.
			name: "custom IFS at empties kept as fields",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: "x"},
				"@":   {Set: true, Kind: Indexed, List: []string{"", "", "", "", ""}},
			},
			src:  `echo =$@=`,
			want: []string{"=", "", "", "", "="},
		},
		{
			// All-empty positional params under whitespace/null IFS
			// produce no fields at all.
			name: "null IFS at all empty drops to nothing",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"@":   {Set: true, Kind: Indexed, List: []string{"", "", ""}},
			},
			src:  `echo $@`,
			want: nil,
		},
		{
			name: "quoted empties preserve split boundaries",
			env: testEnv{
				"A": {Set: true, Kind: String, Str: "   abc   def   "},
			},
			src:  `echo ""$A""`,
			want: []string{"", "abc", "def", ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			word := parseWord(t, tc.src)
			if strings.HasPrefix(tc.src, "echo ") {
				word = parseCallArg(t, tc.src, 1)
			}
			got, err := Fields(&Config{Env: tc.env}, word)
			if err != nil {
				t.Fatalf("%s: did not want error, got %v", tc.src, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
			}
		})
	}
}

func TestFieldsParamExpDefaultBackslashInDoubleQuotes(t *testing.T) {
	cfg := &Config{Env: ListEnviron("somevar=", "HOME=/usr/homes/chet")}
	tests := []struct {
		src  string
		want []string
	}{
		{`echo "${somevar:-\$HOME}"`, []string{"$HOME"}},
		{`echo "${somevar:-\ \$HOME}"`, []string{`\ $HOME`}},
		{`echo "${somevar:-\ \ \$HOME}"`, []string{`\ \ $HOME`}},
	}
	for _, tc := range tests {
		index := 1
		if strings.HasPrefix(tc.src, "echo foo ") {
			index = 2
		}
		word := parseCallArg(t, tc.src, index)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestTildeExpansionHomeTrailingSlash(t *testing.T) {
	tests := []struct {
		name string
		env  string
		src  string
		want []string
	}{
		{
			name: "trailing slash",
			env:  "HOME=/foo/bar/",
			src:  `echo ~ ~/~`,
			want: []string{"/foo/bar/", "/foo/bar/~"},
		},
		{
			name: "root",
			env:  "HOME=/",
			src:  `echo ~ ~/foo`,
			want: []string{"/", "/foo"},
		},
		{
			name: "double slash",
			env:  "HOME=//",
			src:  `echo ~ ~/foo`,
			want: []string{"//", "//foo"},
		},
		{
			name: "assignment",
			env:  "HOME=/foo/bar/",
			src:  `a=~/ b=~/baz`,
			want: []string{"/foo/bar/", "/foo/bar/baz"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Env: ListEnviron(tc.env)}
			if strings.Contains(tc.src, "=") {
				p := syntax.NewParser()
				file, err := p.Parse(strings.NewReader(tc.src), "")
				if err != nil {
					t.Fatal(err)
				}
				call := file.Stmts[0].Cmd.(*syntax.CallExpr)
				var got []string
				for _, as := range call.Assigns {
					s, err := LiteralForAssign(cfg, as.Value)
					if err != nil {
						t.Fatalf("%s: did not want error, got %v", tc.src, err)
					}
					got = append(got, s)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
				}
				return
			}
			var got []string
			for i := range tc.want {
				word := parseCallArg(t, tc.src, i+1)
				fields, err := Fields(cfg, word)
				if err != nil {
					t.Fatalf("%s: did not want error, got %v", tc.src, err)
				}
				got = append(got, fields...)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
			}
		})
	}
}

func TestFieldsParamExpDefaultBackslashFields(t *testing.T) {
	cfg := &Config{Env: ListEnviron("HOME=/usr/homes/chet")}
	tests := []struct {
		src  string
		want []string
	}{
		{`echo ${somevar:-a\ b}`, []string{"a b"}},
		{`echo ${somevar:-a\\b}`, []string{`a\b`}},
		{`echo ${somevar:-\$HOME}`, []string{"$HOME"}},
		{`echo ${somevar:-string \\\}}`, []string{"string", `\}`}},
	}
	for _, tc := range tests {
		index := 1
		if strings.HasPrefix(tc.src, "echo foo ") {
			index = 2
		}
		word := parseCallArg(t, tc.src, index)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsParamExpAssignSingleQuotesInDoubleQuotes(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"foo": {Set: true, Kind: String, Str: "bar"},
	}}
	word := parseCallArg(t, `echo "${fox='$foo'}"`, 1)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"'bar'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestFieldsParamExpPosixSingleQuotesLiteralInDoubleQuotes(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		env        testEnv
		wantAssign string
	}{
		{
			name: "alternate",
			src:  `echo "${a+'x'}"`,
			env: testEnv{
				"a": {Set: true, Kind: String, Str: "set"},
			},
		},
		{
			name: "default",
			src:  `echo "${a-'x'}"`,
			env:  testEnv{},
		},
		{
			name:       "assign",
			src:        `echo "${a='x'}"`,
			env:        testEnv{},
			wantAssign: "'x'",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Env: tc.env, Posix: true}
			word := parseCallArgBashPosix(t, tc.src, 1)
			got, err := Fields(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			want := []string{"'x'"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wanted %q, got %q", want, got)
			}
			if tc.wantAssign != "" {
				if got := cfg.Env.Get("a").String(); got != tc.wantAssign {
					t.Fatalf("assigned a = %q, want %q", got, tc.wantAssign)
				}
			}
		})
	}
}

func TestFieldsParamExpPosixEscapedBrace(t *testing.T) {
	cfg := &Config{Env: ListEnviron("IFS= \t\n")}
	tests := []struct {
		src  string
		want []string
	}{
		{`echo "${IFS+\}z}"`, []string{"}z"}},
		{`echo "${IFS+\"\}\"z}"`, []string{`"}"z`}},
	}
	for _, tc := range tests {
		word := parseCallArg(t, tc.src, 1)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsParamExpAssignQuoteRemoval(t *testing.T) {
	cfg := &Config{Env: testEnv{}}
	word := parseCallArg(t, `echo ${v=a\ b} x ${v=c\ d}`, 1)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
	if v := cfg.Env.Get("v").String(); v != "a b" {
		t.Fatalf("v = %q, want %q", v, "a b")
	}
}

func TestFieldsParamExpAssignLeadingTilde(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"HOME": {Set: true, Kind: String, Str: "/usr/xyz"},
	}}
	word := parseCallArg(t, `echo ${ADDPATH:=~/bin:~/bin2}`, 1)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"/usr/xyz/bin:~/bin2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
	if v := cfg.Env.Get("ADDPATH").String(); v != "/usr/xyz/bin:~/bin2" {
		t.Fatalf("ADDPATH = %q, want %q", v, "/usr/xyz/bin:~/bin2")
	}
}

func TestFieldsParamExpAssignAtNullIFSPosix(t *testing.T) {
	cfg := &Config{
		Env: testEnv{
			"IFS": {Set: true, Kind: String, Str: ""},
			"@":   {Set: true, Kind: Indexed, List: []string{"1", "2"}},
		},
		Posix: true,
	}
	word := parseCallArg(t, `echo ${v=$@}`, 1)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"1 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
	if v := cfg.Env.Get("v").String(); v != "1 2" {
		t.Fatalf("v = %q, want %q", v, "1 2")
	}
}

func TestFieldsParamExpAssignArrayElement(t *testing.T) {
	tests := []struct {
		name      string
		initial   Variable
		src       string
		want      []string
		wantIndex int
		wantKey   string
		wantValue string
	}{
		{
			name:      "indexed element assign",
			initial:   Variable{},
			src:       `${a[42]=foo}`,
			want:      []string{"foo"},
			wantIndex: 42,
			wantValue: "foo",
		},
		{
			name:      "indexed element existing no overwrite",
			initial:   Variable{Set: true, Kind: Indexed, List: []string{"existing"}, ListSet: map[int]bool{0: true}},
			src:       `${a[0]=new}`,
			want:      []string{"existing"},
			wantIndex: 0,
			wantValue: "existing",
		},
		{
			name:      "indexed element colon assign empty",
			initial:   Variable{Set: true, Kind: Indexed, List: []string{""}, ListSet: map[int]bool{0: true}},
			src:       `${a[0]:=new}`,
			want:      []string{"new"},
			wantIndex: 0,
			wantValue: "new",
		},
		{
			name:      "associative element assign",
			initial:   Variable{Kind: Associative, Map: map[string]string{}},
			src:       `${A[key]=value}`,
			want:      []string{"value"},
			wantKey:   "key",
			wantValue: "value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var varName string
			if tc.initial.Kind == Associative {
				varName = "A"
			} else {
				varName = "a"
			}
			env := testEnv{varName: tc.initial}
			cfg := &Config{Env: env}
			word := parseWord(t, tc.src)
			got, err := Fields(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
			vr := cfg.Env.Get(varName)
			if tc.wantKey != "" {
				if got, ok := vr.Map[tc.wantKey]; !ok || got != tc.wantValue {
					t.Fatalf("%s[%s] = %q, want %q", varName, tc.wantKey, got, tc.wantValue)
				}
			}
			if tc.wantIndex >= 0 && tc.wantIndex < len(vr.List) {
				if got := vr.List[tc.wantIndex]; got != tc.wantValue {
					t.Fatalf("%s[%d] = %q, want %q", varName, tc.wantIndex, got, tc.wantValue)
				}
			}
		})
	}
}

func TestFieldsUnsetIndirectArrayKeys(t *testing.T) {
	cfg := &Config{Env: testEnv{}}
	for _, src := range []string{`${!A[*]}`, `${!A[@]}`, `${v=${!A[*]}}`} {
		word := parseWord(t, src)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", src, err)
		}
		if len(got) != 0 {
			t.Fatalf("%s: wanted no fields, got %q", src, got)
		}
	}
}

func TestFieldsParamExpArrayStarSubstWord(t *testing.T) {
	tests := []struct {
		name      string
		ifs       string
		src       string
		want      []string
		wantVar   string
		wantVarOK bool
	}{
		{
			name: "colon default",
			ifs:  ":",
			src:  `echo ${v-${a[*]}}`,
			want: []string{"abc", "def ghi", "jkl"},
		},
		{
			name:    "colon assign",
			ifs:     ":",
			src:     `echo ${v=${a[*]}}`,
			want:    []string{"abc", "def ghi", "jkl"},
			wantVar: "abc:def ghi:jkl", wantVarOK: true,
		},
		{
			name: "empty default",
			ifs:  "",
			src:  `echo ${v-${a[*]}}`,
			want: []string{"abc", "def ghi", "jkl"},
		},
		{
			name:    "empty assign",
			ifs:     "",
			src:     `echo ${v=${a[*]}}`,
			want:    []string{"abcdef ghijkl"},
			wantVar: "abcdef ghijkl", wantVarOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Env: testEnv{
				"IFS": {Set: true, Kind: String, Str: tc.ifs},
				"a":   {Set: true, Kind: Indexed, List: []string{"abc", "def ghi", "jkl"}},
			}}
			word := parseCallArg(t, tc.src, 1)
			got, err := Fields(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
			if tc.wantVarOK {
				if v := cfg.Env.Get("v").String(); v != tc.wantVar {
					t.Fatalf("v = %q, want %q", v, tc.wantVar)
				}
			}
		})
	}
}

func TestFieldsParamExpAlternatePreservesQuotedFields(t *testing.T) {
	cfg := &Config{Env: ListEnviron("IFS= \t\n", "u=x")}
	tests := []struct {
		src  string
		want []string
	}{
		{`echo ${IFS+foo 'b\
ar' baz}`, []string{"foo", "b\\\nar", "baz"}},
		{`echo ${IFS+a" b" c}`, []string{"a b", "c"}},
	}
	for _, tc := range tests {
		word := parseCallArg(t, tc.src, 1)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsParamExpAlternateQuotedAtWithQuotedEmpty(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no args", nil, []string{""}},
		{"one empty", []string{""}, []string{""}},
		{"two empty", []string{"", ""}, []string{"", ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Env: testEnv{
				"x": {Set: true, Kind: String, Str: "x"},
				"@": {Set: true, Kind: Indexed, List: tc.args},
			}}
			for _, src := range []string{
				`echo ${x+"$@"''}`,
				`echo ${x+''"$@"}`,
				`echo ${x+''"$@"''}`,
			} {
				word := parseCallArg(t, src, 1)
				got, err := Fields(cfg, word)
				if err != nil {
					t.Fatalf("%s: did not want error, got %v", src, err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("%s: wanted %q, got %q", src, tc.want, got)
				}
			}
		})
	}
}

func TestFieldsParamExpAlternateQuotedEmptyAfterSplit(t *testing.T) {
	cfg := &Config{Env: testEnv{
		"x": {Set: true, Kind: String, Str: "x"},
		"e": {Set: true, Kind: String, Str: ""},
	}}
	cfg.CmdSubst = func(io.Writer, *syntax.CmdSubst) error { return nil }
	for _, src := range []string{
		`echo ${x:+"$e""$e" ""}`,
		`echo ${x:+"$(:)""$(:)" ""}`,
	} {
		word := parseCallArg(t, src, 1)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", src, err)
		}
		want := []string{"", ""}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: wanted %q, got %q", src, want, got)
		}
	}
}

func TestFieldsParamExpDollarAtStarAlternate(t *testing.T) {
	tests := []struct {
		name string
		env  testEnv
		src  string
		want []string
	}{
		{
			name: "ctlnul_at_alternate",
			env:  testEnv{"@": {Set: true, Kind: Indexed, List: []string{"\x7f"}}},
			src:  `recho "${@:+nonnull}"`,
			want: []string{"nonnull"},
		},
		{
			name: "ctlnul_star_alternate",
			env:  testEnv{"*": {Set: true, Kind: Indexed, List: []string{"\x7f"}}},
			src:  `recho "${*:+nonnull}"`,
			want: []string{"nonnull"},
		},
		{
			name: "null_array_star_alternate",
			env: testEnv{
				"myvar": {Set: true, Kind: Indexed, List: []string{""}, ListSet: map[int]bool{0: true}},
			},
			src:  `recho "${myvar[*]:+nonnull}"`,
			want: []string{""},
		},
		{
			name: "null_array_at_alternate",
			env: testEnv{
				"myvar": {Set: true, Kind: Indexed, List: []string{""}, ListSet: map[int]bool{0: true}},
			},
			src:  `recho "${myvar[@]:+nonnull}"`,
			want: []string{""},
		},
		{
			name: "nonnull_array_star_alternate",
			env: testEnv{
				"myvar": {Set: true, Kind: Indexed, List: []string{"x"}, ListSet: map[int]bool{0: true}},
			},
			src:  `recho "${myvar[*]:+nonnull}"`,
			want: []string{"nonnull"},
		},
		{
			name: "nonnull_array_at_alternate",
			env: testEnv{
				"myvar": {Set: true, Kind: Indexed, List: []string{"x"}, ListSet: map[int]bool{0: true}},
			},
			src:  `recho "${myvar[@]:+nonnull}"`,
			want: []string{"nonnull"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Env: tc.env}
			word := parseCallArg(t, tc.src, 1)
			got, err := Fields(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestParamSliceNegativeLength(t *testing.T) {
	env := testEnv{
		"#": {Set: true, Kind: String, Str: "1"},
		"@": {Set: true, Kind: Indexed, List: []string{"a"}},
		"a": {Set: true, Kind: Indexed, List: []string{"x", "y", "z"}},
		"v": {Set: true, Kind: String, Str: "hello"},
	}
	cfg := &Config{Env: env}
	tests := []struct {
		src     string
		want    []string
		wantErr string
	}{
		{`${@:1:$(($# - 2))}`, nil, "$(($# - 2)): substring expression < 0"},
		{`${@: -3:-2}`, nil, "-2: substring expression < 0"},
		{`${a[@]:0:-2}`, nil, "-2: substring expression < 0"},
		{`${v:1:-2}`, []string{"el"}, ""}, // GNU Bash 5.3: v=hello; ${v:1:-2} -> el
		{`${v: -3:2}`, []string{"ll"}, ""},
		{`${@:2:2}`, []string{"b", "c"}, ""},
	}
	for _, tc := range tests {
		if tc.src == `${@:1:$(($# - 2))}` {
			env["@"] = Variable{Set: true, Kind: Indexed, List: []string{"a"}}
		} else {
			env["@"] = Variable{Set: true, Kind: Indexed, List: []string{"a", "b", "c"}}
		}
		word := parseWord(t, tc.src)
		got, err := Fields(cfg, word)
		if tc.wantErr != "" {
			if err == nil {
				t.Fatalf("%s: wanted error %q, got nil", tc.src, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("%s: wanted error %q, got %q", tc.src, tc.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestBash53VarOpFidelityCluster(t *testing.T) {
	tests := []struct {
		name string
		env  testEnv
		src  string
		want []string
	}{
		{
			name: "uppercase sharp s",
			env:  testEnv{"s": {Set: true, Kind: String, Str: "ß"}},
			src:  `${s@u}:${s@U}:${s^}:${s^^}`,
			want: []string{"ẞ:ẞ:ẞ:ẞ"},
		},
		{
			name: "quoted unset at transform no fields",
			env:  testEnv{},
			src:  `"${u[@]@Q}"`,
			want: nil,
		},
		{
			name: "quoted indexed transform fields",
			env:  testEnv{"a": {Set: true, Kind: Indexed, List: []string{"1", "2", "3"}}},
			src:  `"${a[@]@Q}"`,
			want: []string{"'1'", "'2'", "'3'"},
		},
		{
			name: "quoted indexed star transform joins",
			env:  testEnv{"a": {Set: true, Kind: Indexed, List: []string{"1", "2", "3"}}},
			src:  `"${a[*]@Q}"`,
			want: []string{"'1' '2' '3'"},
		},
		{
			name: "quoted indexed prompt transform fields",
			env:  testEnv{"a": {Set: true, Kind: Indexed, List: []string{"1", "2", "3"}}},
			src:  `"${a[@]@P}"`,
			want: []string{"1", "2", "3"},
		},
		{
			name: "quoted indexed attribute transform fields",
			env:  testEnv{"a": {Set: true, Kind: Indexed, List: []string{"1", "2", "3"}}},
			src:  `"${a[@]@a}"`,
			want: []string{"a", "a", "a"},
		},
		{
			name: "positional attribute transform empty",
			env:  testEnv{"@": {Set: true, Kind: Indexed, List: []string{"1", "2", "3"}}},
			src:  `${@@a}`,
			want: nil,
		},
		{
			name: "quoted assoc transform fields",
			env: testEnv{"A": {
				Set: true, Kind: Associative,
				Map: map[string]string{"a": "hello", "b": "world", "c": "osh", "d": "ysh"},
			}},
			src:  `"${A[@]@P}"`,
			want: []string{"ysh", "osh", "world", "hello"},
		},
		{
			name: "quoted assoc star attribute transform joins",
			env: testEnv{"A": {
				Set: true, Kind: Associative,
				Map: map[string]string{"a": "hello", "b": "world", "c": "osh", "d": "ysh"},
			}},
			src:  `"${A[*]@a}"`,
			want: []string{"A A A A"},
		},
		{
			name: "quoted empty aggregate default",
			env:  testEnv{"a": {Set: true, Kind: Indexed, List: []string{"", ""}}},
			src:  `"${a[@]:-with-colon}"`,
			want: []string{"", ""},
		},
		{
			name: "quoted empty star aggregate default",
			env:  testEnv{"a": {Set: true, Kind: Indexed, List: []string{"", ""}}},
			src:  `"${a[*]:-with-colon}"`,
			want: []string{" "},
		},
		{
			name: "unquoted empty star aggregate default ifs empty",
			env:  testEnv{"IFS": {Set: true, Kind: String, Str: ""}, "a": {Set: true, Kind: Indexed, List: []string{"", ""}}},
			src:  `${a[*]:-empty}`,
			want: nil,
		},
		{
			name: "indirect empty star aggregate default ifs empty",
			env: testEnv{
				"IFS": {Set: true, Kind: String, Str: ""},
				"ref": {Set: true, Kind: String, Str: "a[*]"},
				"a":   {Set: true, Kind: Indexed, List: []string{"", ""}},
			},
			src:  `"${!ref:-with-colon}"`,
			want: []string{""},
		},
		{
			name: "assoc scalar at q and p empty",
			env:  testEnv{"A": {Set: true, Kind: Associative, Map: map[string]string{"x": "y"}}},
			src:  `${A@P}:${A@Q}`,
			want: []string{":"},
		},
		{
			name: "patsub caret in bracket is literal",
			env:  testEnv{"pat": {Set: true, Kind: String, Str: "[^]]"}, "s": {Set: true, Kind: String, Str: "ab^cd^"}},
			src:  `${s//$pat/z}`,
			want: []string{"ab^cd^"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			word := parseCallArg(t, "echo "+tc.src, 1)
			got, err := Fields(&Config{Env: tc.env}, word)
			if err != nil {
				t.Fatalf("%s: did not want error, got %v", tc.src, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
			}
		})
	}
}

func TestPromptTransformBangPosixMode(t *testing.T) {
	tests := []struct {
		name  string
		posix bool
		value string
		want  []string
	}{
		{name: "default bare bang", value: "!", want: []string{"!"}},
		{name: "default escaped bang", value: `\!`, want: []string{"1"}},
		{name: "posix bare bang", posix: true, value: "!", want: []string{"1"}},
		{name: "posix escaped bang", posix: true, value: `\!`, want: []string{"1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			word := parseCallArg(t, `echo "${v@P}"`, 1)
			got, err := Fields(&Config{
				Env:   testEnv{"v": {Set: true, Kind: String, Str: tc.value}},
				Posix: tc.posix,
			}, word)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Fields() = %q, want %q", got, tc.want)
			}
		})
	}
}

type testEnv map[string]Variable

func (e testEnv) Get(name string) Variable {
	return e[name]
}

func (e testEnv) Each(fn func(string, Variable) bool) {
	for name, vr := range e {
		if !fn(name, vr) {
			return
		}
	}
}

func (e testEnv) Set(name string, vr Variable) error {
	e[name] = vr
	return nil
}

func Test_glob(t *testing.T) {
	cfg := &Config{
		ReadDir2: func(string) ([]fs.DirEntry, error) {
			return []fs.DirEntry{
				// The filenames here are sorted, just like [io/fs.ReadDirFS].
				&mockFileInfo{name: "A"},
				&mockFileInfo{name: "AB"},
				&mockFileInfo{name: "a"},
				&mockFileInfo{name: "ab"},
			}, nil
		},
	}

	tests := []struct {
		noCaseGlob bool
		pat        string
		want       []string
	}{
		{false, "a*", []string{"a", "ab"}},
		{false, "A*", []string{"A", "AB"}},
		{false, "*b", []string{"ab"}},
		{false, "b*", nil},
		{true, "a*", []string{"A", "AB", "a", "ab"}},
		{true, "A*", []string{"A", "AB", "a", "ab"}},
		{true, "*b", []string{"AB", "ab"}},
		{true, "b*", nil},
	}
	for _, tc := range tests {
		t.Run(tc.pat, func(t *testing.T) {
			cfg.NoCaseGlob = tc.noCaseGlob
			got, err := cfg.glob("/", tc.pat)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestGlobLeadingPositiveBracketDotfile(t *testing.T) {
	cfg := &Config{
		ReadDir2: func(string) ([]fs.DirEntry, error) {
			return []fs.DirEntry{
				// The filenames here are sorted, just like [io/fs.ReadDirFS].
				&mockFileInfo{name: ".match.580"},
				&mockFileInfo{name: "a.b"},
				&mockFileInfo{name: "x"},
				&mockFileInfo{name: "x.match.580"},
			}, nil
		},
	}

	tests := []struct {
		pat  string
		want []string
	}{
		{"[.]match.580", nil},
		{"[--0]match.580", nil},
		{"[.x]match.580", nil},
		{"[xy].match.580", []string{"x.match.580"}},
		{"a[.]b", []string{"a.b"}},
		{"[x]", []string{"x"}},
	}
	for _, tc := range tests {
		t.Run(tc.pat, func(t *testing.T) {
			got, err := cfg.glob("/", tc.pat)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestFieldsBackslashEscapedGlobMeta(t *testing.T) {
	cfg := &Config{
		ReadDir2: func(string) ([]fs.DirEntry, error) {
			return []fs.DirEntry{
				&mockFileInfo{name: "a"},
				&mockFileInfo{name: "ab"},
			}, nil
		},
	}
	tests := []struct {
		src  string
		want []string
	}{
		{`\*`, []string{"*"}},
		{`*`, []string{"a", "ab"}},
		{`a\*`, []string{"a*"}},
		{`a*`, []string{"a", "ab"}},
	}
	for _, tc := range tests {
		word := parseWord(t, tc.src)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestFieldsFailGlob(t *testing.T) {
	cfg := &Config{
		FailGlob: true,
		ReadDir2: func(string) ([]fs.DirEntry, error) {
			return []fs.DirEntry{&mockFileInfo{name: "a"}}, nil
		},
	}
	word := parseWord(t, `b*`)
	_, err := Fields(cfg, word)
	if err == nil || err.Error() != "no match: b*" {
		t.Fatalf("wanted failglob no-match error, got %v", err)
	}
}

func TestFieldsGlobIgnoreWindowsSlash(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("GLOBIGNORE uses slash-separated patterns on Windows")
	}
	temp := t.TempDir()
	path := filepath.Join(temp, "ab", "cd", "efg")
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Env:      ListEnviron("PWD="+temp, "GLOBIGNORE=ab/cd/efg"),
		ReadDir2: os.ReadDir,
	}
	got, err := Fields(cfg, parseWord(t, `*/*/efg*`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`*/*/efg*`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GLOBIGNORE pathname filter: got %q, want %q", got, want)
	}
}

func TestFieldsNullGlobInvalidBracketWithSlash(t *testing.T) {
	cfg := &Config{
		NullGlob: true,
		ReadDir2: func(string) ([]fs.DirEntry, error) {
			return nil, nil
		},
	}
	word := parseWord(t, `[qwe\/qwe]`)
	got, err := Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if got != nil {
		t.Fatalf("wanted no fields, got %q", got)
	}

	word = parseWord(t, `[qwe\/`)
	got, err = Fields(cfg, word)
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"[qwe/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestFieldsBackslashEscapedGlobPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is the path separator on windows, not a glob escape; glob results use \\")
	}
	temp := t.TempDir()
	if err := os.MkdirAll(temp+"/tmp/a/b", 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temp+"/tmp/a/b/c", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Env:      ListEnviron("PWD="+temp, `bs=\`),
		ReadDir2: os.ReadDir,
	}
	tests := []struct {
		src  string
		want []string
	}{
		{`./tmp\/a/b/*`, []string{"./tmp/a/b/c"}},
		{`./t\mp/a/b/*`, []string{"./tmp/a/b/c"}},
		{`./tmp${bs}/a/b/*`, []string{"./tmp/a/b/c"}},
		{`./tm[p]${bs}/a/b/c`, []string{`./tm[p]\/a/b/c`}},
	}
	for _, tc := range tests {
		word := parseWord(t, tc.src)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

// TestFieldsGlobOrphanTrailingBracket covers VSC-PCTS #575: a path
// component ending in a `[` with no closing `]` has a literal trailing `[`
// in bash when the component is otherwise a glob, so `d*[` matches a
// directory literally named `dir[`. Previously bashy translated the
// trailing `[` as an unterminated bracket and matched nothing.
func TestFieldsGlobOrphanTrailingBracket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("[ and ] are not valid characters in Windows filenames")
	}
	temp := t.TempDir()
	// A directory literally named "dir[" holding a file named "]f".
	if err := os.MkdirAll(temp+"/dir[", 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temp+"/dir[/]f", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Env:      ListEnviron("PWD=" + temp),
		ReadDir2: os.ReadDir,
	}
	tests := []struct {
		src  string
		want []string
	}{
		{`./d*[/]f`, []string{"./dir[/]f"}},
		{`./di?[/]f`, []string{"./dir[/]f"}},
		{`./d*[/]*`, []string{"./dir[/]f"}},
		{`./dir[/]f`, []string{"./dir[/]f"}}, // fully literal path
	}
	for _, tc := range tests {
		word := parseWord(t, tc.src)
		got, err := Fields(cfg, word)
		if err != nil {
			t.Fatalf("%s: did not want error, got %v", tc.src, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: wanted %q, got %q", tc.src, tc.want, got)
		}
	}
}

func TestGlobSort(t *testing.T) {
	temp := t.TempDir()
	files := []struct {
		name string
		size int
	}{
		{"mksyntax", 4},
		{"mksignames", 7},
		{"make_cmd.o", 11},
		{"mailcheck.o", 13},
		{"mksignames.o", 16},
		{"mksyntax.dSYM", 19},
	}
	baseTime := time.Now().Add(-time.Hour).Round(0)
	for i, file := range files {
		data := strings.Repeat("x", file.size)
		path := filepath.Join(temp, file.name)
		if err := os.WriteFile(path, []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
		mtime := baseTime.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{
		Env:      ListEnviron("PWD=" + temp),
		ReadDir2: os.ReadDir,
	}
	tests := []struct {
		globSort string
		want     []string
	}{
		{"", []string{"mailcheck.o", "make_cmd.o", "mksignames", "mksignames.o", "mksyntax", "mksyntax.dSYM"}},
		{"-name", []string{"mksyntax.dSYM", "mksyntax", "mksignames.o", "mksignames", "make_cmd.o", "mailcheck.o"}},
		{"size", []string{"mksyntax", "mksignames", "make_cmd.o", "mailcheck.o", "mksignames.o", "mksyntax.dSYM"}},
		{"-size", []string{"mksyntax.dSYM", "mksignames.o", "mailcheck.o", "make_cmd.o", "mksignames", "mksyntax"}},
		{"+nonsense", []string{"mailcheck.o", "make_cmd.o", "mksignames", "mksignames.o", "mksyntax", "mksyntax.dSYM"}},
	}
	for _, tc := range tests {
		t.Run(tc.globSort, func(t *testing.T) {
			cfg.Env = ListEnviron("PWD="+temp, "GLOBSORT="+tc.globSort)
			got, err := cfg.glob(temp, "m*")
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestGlobSearchableDot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	temp := t.TempDir()
	if err := os.Mkdir(filepath.Join(temp, "searchable"), 0o111); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(temp, "readable"), 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(temp, "searchable"), 0o777)
	defer os.Chmod(filepath.Join(temp, "readable"), 0o777)
	cfg := &Config{
		Env:      ListEnviron("PWD=" + temp),
		ReadDir2: os.ReadDir,
	}
	got, err := cfg.glob(temp, "*/.")
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"searchable/."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

func TestGlobLiteralUnreadableIntermediateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses unix-style / path separators; windows glob results use \\")
	}
	cfg := &Config{
		ReadDir2: func(path string) ([]fs.DirEntry, error) {
			switch filepath.ToSlash(path) {
			case "/tmp":
				return nil, nil
			case "/tmp/a":
				return nil, fs.ErrPermission
			case "/tmp/a/b":
				return []fs.DirEntry{&mockFileInfo{name: "c"}}, nil
			default:
				return nil, fs.ErrNotExist
			}
		},
	}
	got, err := cfg.glob("/", "tmp/a/b/*")
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"tmp/a/b/c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
	got, err = cfg.glob("/", "tmp/a/*")
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if got != nil {
		t.Fatalf("wanted no matches, got %q", got)
	}
}

func TestGlobNoSearchIntermediateDirBeforeGlob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses unix-style / path separators; windows glob results use \\")
	}
	cfg := &Config{
		ReadDir2: func(path string) ([]fs.DirEntry, error) {
			switch filepath.ToSlash(path) {
			case "/tmp":
				return []fs.DirEntry{&mockFileInfo{name: "foo", typ: os.ModeDir | 0o755}}, nil
			case "/tmp/foo":
				return []fs.DirEntry{&mockFileInfo{name: "no_search_dir", typ: os.ModeDir | 0o600}}, nil
			case "/tmp/foo/no_search_dir":
				return []fs.DirEntry{&mockFileInfo{name: "file"}}, nil
			default:
				return nil, fs.ErrNotExist
			}
		},
	}
	got, err := cfg.glob("/", "tmp/foo/no_search_d*r/file")
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	if got != nil {
		t.Fatalf("wanted no matches, got %q", got)
	}
	got, err = cfg.glob("/", "tmp/foo/no_search_d*r/f*e")
	if err != nil {
		t.Fatalf("did not want error, got %v", err)
	}
	want := []string{"tmp/foo/no_search_dir/file"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted %q, got %q", want, got)
	}
}

type mockFileInfo struct {
	name        string
	typ         fs.FileMode
	fs.DirEntry // Stub out everything but Name() & Type()
}

var _ fs.DirEntry = (*mockFileInfo)(nil)

func (fi *mockFileInfo) Name() string      { return fi.name }
func (fi *mockFileInfo) Type() fs.FileMode { return fi.typ }
func (fi *mockFileInfo) Info() (fs.FileInfo, error) {
	return fileInfoDirEntry{fi}, nil
}

type fileInfoDirEntry struct {
	*mockFileInfo
}

func (fi fileInfoDirEntry) Size() int64        { return 0 }
func (fi fileInfoDirEntry) Mode() fs.FileMode  { return fi.typ }
func (fi fileInfoDirEntry) ModTime() time.Time { return time.Time{} }
func (fi fileInfoDirEntry) IsDir() bool        { return fi.typ.IsDir() }
func (fi fileInfoDirEntry) Sys() any           { return nil }

func TestAnchoredEmptyPatternSubst(t *testing.T) {
	tests := []struct {
		env  string
		src  string
		want string
	}{
		{"var=blah", `${var/#/--}`, "--blah"},
		{"var=abc", `${var/#/x}`, "xabc"},
		{"var=", `${var/#/x}`, "x"},
		{"var=abc", `${var/%/x}`, "abcx"},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			cfg := &Config{Env: ListEnviron(tc.env)}
			word := parseWord(t, tc.src)
			got, err := Literal(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestGlobalAnchoredPatternSubst(t *testing.T) {
	tests := []struct {
		env  testEnv
		src  string
		want string
	}{
		{
			env: testEnv{
				"var": {Set: true, Kind: Indexed, List: []string{"abcde", "abcdf"}},
			},
			src:  `${var[*]//#abc/foo}`,
			want: "abcde abcdf",
		},
		{
			env: testEnv{
				"var": {Set: true, Kind: Indexed, List: []string{"abcde", "abcdf"}},
			},
			src:  `${var[*]/#abc/foo}`,
			want: "foode foodf",
		},
		{
			env: testEnv{
				"v": {Set: true, Kind: String, Str: "x#abcy"},
			},
			src:  `${v//#abc/Z}`,
			want: "xZy",
		},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			cfg := &Config{Env: tc.env}
			word := parseWord(t, tc.src)
			got, err := Literal(cfg, word)
			if err != nil {
				t.Fatalf("did not want error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}
