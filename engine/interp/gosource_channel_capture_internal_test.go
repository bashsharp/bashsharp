package interp

import "testing"

func TestGoSourceChannelCaptureUsesTypedOperands(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"receive_selector", `<-box.C`, []string{"box"}},
		{"send_selectors", `box.C <- payload.N`, []string{"box", "payload"}},
		{"short_receive_selector", `value:=<-box.C;_=value`, []string{"box"}},
		{"select_receive", `select {case value:=<-box.C:_=value;default:}`, []string{"box"}},
		{"select_receive_tuple", `select {case value,ok:=<-box.C:_,_=value,ok;default:}`, []string{"box"}},
		{"select_receive_assignment", `select {case payload.N=<-box.C:default:}`, []string{"box", "payload"}},
		{"select_case_shadow", `select {case payload:=<-box.C:_=payload;default:}`, []string{"box"}},
		{"select_computed_receive", `select {case value:=<-pick(box.C):_=value;default:}`, []string{"box"}},
		{"select_send", `select {case box.C <- payload.N:default:}`, []string{"box", "payload"}},
		{"computed_receive", `<-pick(box.C)`, []string{"box"}},
		{"shadow_and_literal", `box:=struct{C chan int}{};_="payload";<-box.C`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := `package main;func pick(c chan int)chan int{return c};func main(){box:=struct{C chan int}{};payload:=struct{N int}{};_=box;_=payload;go func(){` + tc.body + `}()}`
			r, cells := captureRunnerFor("box", "payload", "C", "N")
			g := goSourceGoStmt(t, source)
			shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
			if r.exit.err != nil {
				t.Fatal(r.exit.err)
			}
			want := map[string]bool{}
			for _, name := range tc.want {
				want[name] = true
			}
			for name, cell := range cells {
				if shared[cell] != want[name] {
					t.Errorf("capture %s=%v want %v", name, shared[cell], want[name])
				}
			}
		})
	}
}
