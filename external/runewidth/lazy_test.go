package runewidth

import (
	"os"
	"os/exec"
	"testing"
)

// The subprocess starts with a fresh package graph, independently of test
// ordering. These are v0.0.27 width results used by bubbles/ycode, including
// an East Asian ambiguous rune and an emoji-neutral option.
func TestLazyWidthParity(t *testing.T) {
	if os.Getenv("RUNEWIDTH_LAZY_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLazyWidthParity$")
		cmd.Env = append(os.Environ(), "RUNEWIDTH_LAZY_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fresh-process parity: %v\n%s", err, out)
		}
		return
	}
	if len(zerowidth) != 0 || len(eastAsianWidth) != 0 {
		t.Fatal("width tables were built during package initialization")
	}
	for _, tc := range []struct {
		r     rune
		plain int
		cjk   int
		emoji int
	}{
		{'a', 1, 1, 1},
		{'\u0300', 0, 0, 0},
		{'☆', 1, 2, 2},
		{'世', 2, 2, 2},
		{'☺', 1, 1, 2},
	} {
		c := &Condition{StrictEmojiNeutral: true}
		if got := c.RuneWidth(tc.r); got != tc.plain {
			t.Errorf("plain width %U: %d, want %d", tc.r, got, tc.plain)
		}
		c.EastAsianWidth = true
		if got := c.RuneWidth(tc.r); got != tc.cjk {
			t.Errorf("CJK width %U: %d, want %d", tc.r, got, tc.cjk)
		}
		c.StrictEmojiNeutral = false
		if got := c.RuneWidth(tc.r); got != tc.emoji {
			t.Errorf("emoji-neutral width %U: %d, want %d", tc.r, got, tc.emoji)
		}
	}
	if len(zerowidth) == 0 || len(eastAsianWidth) == 0 {
		t.Fatal("first width query did not build the width tables")
	}
}
