//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGoSourceOriginalNativeTickerThreeModes(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-native-channels/tickers.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != "9dfd404595ed484a3f4e19ec9ab712d2d3451d2555f75197c94296af234d532f" {
		t.Fatal("original bytes changed")
	}
	start := time.Now()
	stamp := regexp.MustCompile(`^Tick at (.+) m=\+([0-9]+\.[0-9]+)$`)
	normalize := func(output string) string {
		t.Helper()
		t.Logf("original output: %q", output)
		lines := strings.Split(output, "\n")
		if len(lines) != 5 || lines[3] != "Ticker stopped" || lines[4] != "" {
			t.Fatalf("expected exactly three ticks then stopped: %q", output)
		}
		var previous time.Time
		var previousMono float64
		for _, line := range lines[:3] {
			match := stamp.FindStringSubmatch(line)
			if match == nil {
				t.Fatalf("malformed original tick: %q", line)
			}
			wall, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", match[1])
			if err != nil || wall.Before(start) || wall.After(time.Now()) {
				t.Fatalf("tick outside actual observation window: %q (%v)", line, err)
			}
			mono, err := strconv.ParseFloat(match[2], 64)
			if err != nil || mono <= 0 || (!previous.IsZero() && (!wall.After(previous) || mono <= previousMono)) {
				t.Fatalf("non-increasing tick timestamps: %q", output)
			}
			previous, previousMono = wall, mono
		}
		return "Tick at <observed time>\nTick at <observed time>\nTick at <observed time>\nTicker stopped\n"
	}
	typedSendThreeModesNormalized(t, string(source), normalize)
}
