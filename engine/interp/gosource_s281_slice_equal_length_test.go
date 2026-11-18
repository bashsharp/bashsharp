//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

// TestS281SlicesEqualVisibleLength compares generic slices.Equal against native
// Go for explicit len/cap distinctions. The bridge intentionally transports
// capacity tails for later writeback; equality observes only each slice view.
func TestS281SlicesEqualVisibleLength(t *testing.T) {
	for name, body := range map[string]string{
		"literal and appended positive negative ints": `literal:=[]int{1,-2,3};appended:=make([]int,0,8);appended=append(appended,1,-2,3);fmt.Println(slices.Equal(literal,appended))`,
		"literal and appended unequal":                `literal:=[]int{1,-2,3};appended:=make([]int,0,8);appended=append(appended,1,-2,4);fmt.Println(slices.Equal(literal,appended))`,
		"capacity equals length":                      `a:=[]int{1,-2,3};b:=append([]int(nil),1,-2,3);a=a[:len(a):len(a)];b=b[:len(b):len(b)];fmt.Println(slices.Equal(a,b))`,
		"nil and empty":                               `var a []int;b:=[]int{};fmt.Println(slices.Equal(a,b))`,
		"same payload unequal visible lengths":        `back:=[]int{1,-2,3};a:=back[:2:3];b:=back[:3:3];fmt.Println(slices.Equal(a,b))`,
		"unequal visible value":                       `a:=[]int{1,-2,3};b:=[]int{1,-9,3};fmt.Println(slices.Equal(a,b))`,
		"equal views differing capacity tails":        `ab:=[]int{1,-2,7};bb:=[]int{1,-2,8};a:=ab[:2:3];b:=bb[:2:3];fmt.Println(slices.Equal(a,b))`,
		"aliases and named slices":                    `type ints []int;base:=ints{1,-2,7};a:=base[:2:3];b:=base[:2:2];fmt.Println(slices.Equal(a,b))`,
	} {
		t.Run(name, func(t *testing.T) {
			differGoSource(t, `package main
import (
	"fmt"
	"slices"
)
func main() { `+body+` }
`, nil, "")
		})
	}
}
