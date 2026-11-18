package interactiveprobe

import "testing"

func TestStatusRequiresExecutedMarker(t *testing.T) {
	for _, s := range []string{"printf '%s%s:%d\\n' '__DEPTH_' 'test' \"$?\"", "__DEPTH_test:0suffix\n", "prefix__DEPTH_test:0\n"} {
		if _, ok := status(s, "test"); ok {
			t.Fatalf("accepted echoed or partial marker %q", s)
		}
	}
	if n, ok := status("\x1b[0m__DEPTH_test:7\r\n", "test"); !ok || n != 7 {
		t.Fatalf("status = %d, %v", n, ok)
	}
}

func TestConPTYPromptAndIncompleteMarker(t *testing.T) {
	if !ready("\x1b[?25hDEPTH_READY>\x1b[K\x1b[1C") {
		t.Fatal("ConPTY represents trailing prompt space as cursor motion")
	}
	if _, ok := status("__DEPTH_test:0", "test"); ok {
		t.Fatal("accepted an incomplete output line")
	}
}

func TestConPTYCursorPositionStartsOutputLine(t *testing.T) {
	s := "echoed input\x1b[3;1H__DEPTH_test:0\r\nDEPTH_READY>\x1b[K\x1b[1C"
	if n, ok := status(s, "test"); !ok || n != 0 {
		t.Fatalf("status = %d, %v", n, ok)
	}
}
