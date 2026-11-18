package polyglot

import "testing"

func TestWorkerExitStatus(t *testing.T) {
	for _, runtime := range []string{"Go", "Python", "TypeScript", "Rust", "C", "C++", "C#", "PowerShell"} {
		for _, code := range []int{0, 3, 42} {
			death := &WorkerExit{Runtime: runtime, Code: code}
			want := 1
			if runtime == "Go" && code > 0 {
				want = code
			}
			if got := death.ExitStatus(); got != want {
				t.Errorf("%s exit %d: got %d, want %d", runtime, code, got, want)
			}
		}
		death := &WorkerExit{Runtime: runtime, Signal: 9, Code: -1}
		if got := death.ExitStatus(); got != 1 {
			t.Errorf("%s signal death: got %d, want 1", runtime, got)
		}
	}
}
