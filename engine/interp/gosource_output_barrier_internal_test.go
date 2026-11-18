package interp

import "testing"

func TestGoSourceNoOutputBridgeReplies(t *testing.T) {
	req := bashPPEvalRequest{Imports: map[string]string{"clock": "time", "fmt": "fmt", "other": "example.org/other"}}
	defaultReply := bashPPBridgeResponse{Values: []bashPPBridgeValue{{Kind: "int", Text: "-1"}, {}, {}}}
	readyReply := bashPPBridgeResponse{Values: []bashPPBridgeValue{{Kind: "int", Text: "0"}, {}, {}}}
	for _, tc := range []struct {
		name  string
		q     bashPPBridgeRequest
		reply bashPPBridgeResponse
		want  bool
	}{
		{"select default", bashPPBridgeRequest{Op: "channel-select", Selector: "probe"}, defaultReply, true},
		{"select receive", bashPPBridgeRequest{Op: "channel-select", Selector: "probe"}, readyReply, false},
		{"time since", bashPPBridgeRequest{Op: "call", Selector: "clock.Since"}, bashPPBridgeResponse{}, true},
		{"other since", bashPPBridgeRequest{Op: "call", Selector: "other.Since"}, bashPPBridgeResponse{}, false},
		{"duration round", bashPPBridgeRequest{Op: "call", Selector: "Round", Receiver: &bashPPBridgeValue{Type: "time.Duration"}}, bashPPBridgeResponse{}, true},
		{"other round", bashPPBridgeRequest{Op: "call", Selector: "Round", Receiver: &bashPPBridgeValue{Type: "other.Duration"}}, bashPPBridgeResponse{}, false},
		{"fmt print", bashPPBridgeRequest{Op: "call", Selector: "fmt.Printf"}, bashPPBridgeResponse{}, false},
		{"error", bashPPBridgeRequest{Op: "call", Selector: "clock.Since"}, bashPPBridgeResponse{Error: "failure"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bashPPNativeNoOutputReply(req, tc.q, tc.reply); got != tc.want {
				t.Fatalf("no-output bridge reply = %v, want %v", got, tc.want)
			}
		})
	}
}
