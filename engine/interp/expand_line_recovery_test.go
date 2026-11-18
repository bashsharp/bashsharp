package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashExpansionErrorInputLineRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, src, file, want string
		commandString, posix  bool
	}{
		{
			name: "command string one line", commandString: true, file: "bash",
			src:  "echo $(( 1 / 0 )); echo after",
			want: "bash: line 1: 1 / 0 : division by 0 (error token is \"0 \")\nexit status 1",
		},
		{
			name: "command string next line", commandString: true, file: "bash",
			src:  "echo $(( 1 / 0 ))\necho nextline",
			want: "bash: line 1: 1 / 0 : division by 0 (error token is \"0 \")\nnextline\n",
		},
		{
			name: "script next line", file: "script.sh",
			src:  "echo $(( 1 / 0 )); echo same-line\necho next-line",
			want: "script.sh: line 1: 1 / 0 : division by 0 (error token is \"0 \")\nnext-line\n",
		},
		{
			name: "POSIX script exits", file: "script.sh", posix: true,
			src:  "echo $(( 1 / 0 )); echo same-line\necho next-line",
			want: "script.sh: line 1: 1 / 0 : division by 0 (error token is \"0 \")\nexit status 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(tc.src), tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			r, err := interp.New(interp.StdIO(nil, &output, &output), interp.WithBashCompatErrors(true),
				interp.WithBashSource([]byte(tc.src)), interp.CommandString(tc.commandString), interp.WithPosixMode(tc.posix))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), file)
			got := output.String()
			if err != nil {
				got += err.Error()
			}
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}
