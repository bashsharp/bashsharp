package interp

import "testing"

func TestGoSourceLocalTimeMillisecond(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	req := bashPPEvalRequest{Imports: map[string]string{"clock": "time", "other": "example.org/other"}}
	for _, tc := range []struct {
		name string
		q    bashPPBridgeRequest
		want bool
	}{
		{"renamed time import", bashPPBridgeRequest{Op: "get", Selector: "clock.Millisecond"}, true},
		{"other package", bashPPBridgeRequest{Op: "get", Selector: "other.Millisecond"}, false},
		{"other constant", bashPPBridgeRequest{Op: "get", Selector: "clock.Second"}, false},
		{"call", bashPPBridgeRequest{Op: "call", Selector: "clock.Millisecond"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, handled := r.goSourceLocalTimeMillisecond(req, tc.q)
			if handled != tc.want {
				t.Fatalf("handled=%v, want %v", handled, tc.want)
			}
			if handled && (value.Kind != "int" || value.Type != "time.Duration" || value.Text != "1000000") {
				t.Fatalf("Millisecond=%+v", value)
			}
		})
	}
}

func TestGoSourceLocalDurationRound(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	for _, tc := range []struct {
		name     string
		duration string
		unit     string
		want     string
	}{
		{"below half", "1499", "1000", "1000"},
		{"at half", "1500", "1000", "2000"},
		{"negative half", "-1500", "1000", "-2000"},
		{"nonpositive unit", "1500", "0", "1500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, handled := r.goSourceLocalDurationRound(bashPPBridgeRequest{
				Op: "call", Selector: "Round",
				Receiver: &bashPPBridgeValue{Kind: "int", Type: "time.Duration", Text: tc.duration},
				Args:     []bashPPBridgeValue{{Kind: "int", Type: "time.Duration", Text: tc.unit}},
			})
			if !handled || value.Kind != "int" || value.Type != "time.Duration" || value.Text != tc.want {
				t.Fatalf("Round: handled=%v value=%+v, want %s", handled, value, tc.want)
			}
		})
	}
	r.bashPPGoTask = true
	if _, handled := r.goSourceLocalDurationRound(bashPPBridgeRequest{
		Op: "call", Selector: "Round",
		Receiver: &bashPPBridgeValue{Kind: "int", Type: "time.Duration", Text: "1"},
		Args:     []bashPPBridgeValue{{Kind: "int", Type: "time.Duration", Text: "1"}},
	}); handled {
		t.Fatal("launched task must keep dependency request path")
	}
}
