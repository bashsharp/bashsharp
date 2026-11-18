//go:build full

package interp

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestS248SharedOrderingStaleAndForged replays a prepared shared-ordering
// request whose callback or receiver identity no longer names this session's
// storage: after Reset (stale) and with a handle the session never minted
// (forged). Each must refuse before any Less or Swap touches storage.
func TestS248SharedOrderingStaleAndForged(t *testing.T) {
	for name, tc := range map[string]struct {
		setup, fun string
		args       []string
		forge      func(*bashPPBridgeRequest)
		want       string
	}{
		"stale sort.Slice callback": {
			setup: "s:=[]int{3,1,2};less:=func(i,j int)bool{return s[i]<s[j]};_=less",
			fun:   "Slice", args: []string{"s", "less"},
			want: "a current original function callback is required",
		},
		"stale sort.Sort pointer receiver": {
			setup: "b:=&box{xs:[]int{3,1,2}};_=b",
			fun:   "Sort", args: []string{"b"},
			want: "another dependency session",
		},
		"forged sort.Slice callback handle": {
			setup: "s:=[]int{3,1,2};less:=func(i,j int)bool{return s[i]<s[j]};_=less",
			fun:   "Slice", args: []string{"s", "less"},
			forge: func(q *bashPPBridgeRequest) { q.Args[1].Handle += 1000 },
			want:  "original callback handle expired",
		},
		"forged sort.Sort receiver origin": {
			setup: "b:=&box{xs:[]int{3,1,2}};_=b",
			fun:   "Sort", args: []string{"b"},
			forge: func(q *bashPPBridgeRequest) { q.Args[0].Origin += 1000 },
			want:  "receiver identity expired",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var r *Runner
			var old bashPPBridgeRequest
			var probeErr error
			round := 0
			rounds := 2
			if tc.forge != nil {
				rounds = 1
			}
			replay := func() {
				req, err := r.bashPPEvalRequest()
				if err != nil {
					probeErr = err
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, probeErr = r.bashPPNativeRequest(ctx, req, old)
			}
			writer := callbackProbeWriter(func(p []byte) (int, error) {
				if !bytes.Contains(p, []byte("capture")) {
					return len(p), nil
				}
				if round == 0 {
					call := &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "sort"}, {Value: tc.fun}}}
					for _, arg := range tc.args {
						lit := &syntax.Lit{Value: arg}
						call.Args = append(call.Args, &syntax.Word{Parts: []syntax.WordPart{lit}})
						call.ArgExprs = append(call.ArgExprs, &syntax.BashPPIdent{Name: lit})
					}
					old, probeErr = r.bashPPPrepareNativeCall(context.Background(), call)
					if probeErr == nil && tc.forge != nil {
						tc.forge(&old)
						replay()
					}
				} else {
					replay()
				}
				return len(p), nil
			})
			var err error
			r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
			if err != nil {
				t.Fatal(err)
			}
			// The probe names the program's variables from outside it, after
			// their last use in the source; keep them live past the capture so
			// statement-level binding release does not drop them first.
			var uses []string
			for _, arg := range tc.args {
				uses = append(uses, "_ = "+arg)
			}
			keepAlive := strings.Join(uses, ";")
			for round = 0; round < rounds; round++ {
				if round > 0 {
					r.Reset()
				}
				source := fmt.Sprintf(`package main
import "sort"
type box struct{xs []int}
func (b *box) Len() int { return len(b.xs) }
func (b *box) Less(i, j int) bool { return b.xs[i] < b.xs[j] }
func (b *box) Swap(i, j int) { b.xs[i], b.xs[j] = b.xs[j], b.xs[i] }
func main(){%s;println("capture");sort.Ints(nil);%s}`, tc.setup, keepAlive)
				p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
				if err != nil {
					t.Fatal(err)
				}
				if err = r.Run(context.Background(), p.File); err != nil && tc.forge == nil {
					t.Fatal(err)
				}
				if round == 0 && tc.forge == nil && probeErr != nil {
					t.Fatal(probeErr)
				}
			}
			if probeErr == nil || !strings.Contains(probeErr.Error(), tc.want) {
				t.Fatalf("replayed identity accepted: %v", probeErr)
			}
			if view := old.Args[0].sliceView; view != nil && fmt.Sprint(view.view) != "[3 1 2]" {
				t.Fatalf("refused request still permuted storage: %v", view.view)
			}
		})
	}
}
