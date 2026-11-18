//go:build full

package interp_test

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Run remotely with -benchtime=2000x. Each iteration is a send + receive;
// report communication operations, not pairs. The program clock excludes
// parsing, worker compilation/startup and the initial channel handshake.
// Both modes execute the same interpreted AST; native disables only the
// allocation certificate, never interpreter execution.
func BenchmarkS374ResidentChannels(b *testing.B) {
	for _, kind := range []string{"buffered", "field", "rendezvous"} {
		for _, domain := range []string{"native", "resident"} {
			b.Run(kind+"/"+domain, func(b *testing.B) {
				body := `c:=make(chan int,1);c<-0;<-c;start:=time.Now();for i:=0;i<%d;i++{c<-i;<-c};fmt.Println(time.Since(start).Nanoseconds())`
				if kind == "rendezvous" {
					body = `c:=make(chan int);done:=make(chan bool,1);go func(){for i:=0;i<%d+1;i++{c<-i};done<-true}();<-c;start:=time.Now();for i:=0;i<%d;i++{<-c};elapsed:=time.Since(start).Nanoseconds();<-done;fmt.Println(elapsed)`
					body = fmt.Sprintf(body, b.N, b.N)
				} else {
					body = fmt.Sprintf(body, b.N)
				}
				if kind == "field" {
					body = strings.ReplaceAll(body, "c:=make(chan int,1)", "box:=new(Box);box.c=make(chan int,1)")
					body = strings.ReplaceAll(body, "c<-", "box.c<-")
					body = strings.ReplaceAll(body, "<-c", "<-box.c")
				}
				source := `package main;import("time";"fmt");type Box struct{c chan int};func main(){` + body + `}`
				p, err := gosource.Parse(strings.NewReader(source), "throughput.go", gosource.Options{RunMain: true})
				if err != nil {
					b.Fatal(err)
				}
				seen := 0
				syntax.Walk(p.File, func(n syntax.Node) bool {
					if ch, ok := n.(*syntax.BashPPChanType); ok {
						seen++
						if domain == "native" {
							ch.LocalDomain = false
						} else if !ch.LocalDomain {
							b.Fatal("resident benchmark lost certificate")
						}
					}
					return true
				})
				if seen == 0 {
					b.Fatal("no channels")
				}
				var out, errout bytes.Buffer
				r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(b.TempDir()), interp.StdIO(nil, &out, &errout))
				if err != nil {
					b.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err = r.Run(ctx, p.File); err != nil {
					b.Fatalf("%v: %s", err, errout.String())
				}
				elapsed, err := strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
				if err != nil || elapsed <= 0 {
					b.Fatalf("bad duration %q: %v", out.String(), err)
				}
				b.ReportMetric(float64(elapsed)/float64(2*b.N), "ns/op")
				b.ReportMetric(float64(2*b.N)*1e9/float64(elapsed), "chan-ops/s")
			})
		}
	}
}

func TestS374ResidentCancellation(t *testing.T) {
	for _, declaration := range []string{"c:=make(chan int)", "var c chan int"} {
		for _, operation := range []string{"<-c", "c<-7", "select{case <-c:}", "for range c{}"} {
			t.Run(declaration+"/"+operation, func(t *testing.T) {
				source := `package main;import("fmt";"time");func main(){go func(){for{time.Sleep(time.Hour)}}();` + declaration + `;fmt.Println("ready");` + operation + `;fmt.Println("UNREACHABLE")}`
				p, err := gosource.Parse(strings.NewReader(source), "cancel.go", gosource.Options{RunMain: true})
				if err != nil {
					t.Fatal(err)
				}
				syntax.Walk(p.File, func(n syntax.Node) bool {
					if ch, ok := n.(*syntax.BashPPChanType); ok && !ch.LocalDomain {
						t.Fatal("not a resident cancellation test")
					}
					return true
				})
				out := &sendReadyWriter{marker: "ready\n", ready: make(chan struct{})}
				var stderr bytes.Buffer
				r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, out, &stderr))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- r.Run(ctx, p.File) }()
				select {
				case <-out.ready:
				case err := <-done:
					t.Fatalf("before ready: %v", err)
				case <-ctx.Done():
					t.Fatal("readiness timeout")
				}
				select {
				case err := <-done:
					t.Fatalf("did not block: %v stderr=%q", err, stderr.String())
				case <-time.After(20 * time.Millisecond):
				}
				cancel()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancellation succeeded")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("channel survived cancellation")
				}
				if out.String() != "ready\n" {
					t.Fatalf("post-cancellation execution: %q", out.String())
				}
			})
		}
	}
}
