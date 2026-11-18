// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build windows

package interp

import (
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
)

func TestBashPPCallbackMailboxWindowsRoundTripAndCloseLease(t *testing.T) {
	mailbox, err := newBashPPCallbackMailbox()
	if err != nil {
		t.Fatal(err)
	}
	if !mailbox.retain() {
		t.Fatal("new mailbox refused request lease")
	}

	want := bashPPBridgeResponse{ID: 42, Op: "callback", Selector: "Image.At", Receiver: &bashPPBridgeValue{Kind: "struct", Type: "main.Image"}}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	copy(mailbox.requestBytes(0), payload)
	atomic.StoreUint32(mailbox.word(0, 4), uint32(len(payload)))
	atomic.StoreUint32(mailbox.word(0, 0), bashPPMailboxRequest)

	slot, got, ok := mailbox.take()
	if !ok || slot != 0 || got.ID != want.ID || got.Selector != want.Selector || got.Receiver == nil || got.Receiver.Type != want.Receiver.Type {
		t.Fatalf("mailbox request: slot=%d ok=%v got=%+v", slot, ok, got)
	}
	if !mailbox.answer(slot, bashPPBridgeRequest{ID: got.ID, Op: "callback-reply", Values: []bashPPBridgeValue{{Kind: "int", Text: "7"}}}) {
		t.Fatal("mailbox reply did not fit")
	}
	if state := atomic.LoadUint32(mailbox.word(0, 0)); state != bashPPMailboxReply {
		t.Fatalf("mailbox state = %d, want reply", state)
	}
	n := int(atomic.LoadUint32(mailbox.word(0, 8)))
	var reply bashPPBridgeRequest
	if err := json.Unmarshal(mailbox.replyBytes(0)[:n], &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != 42 || len(reply.Values) != 1 || reply.Values[0].Text != "7" {
		t.Fatalf("mailbox reply: %+v", reply)
	}

	path := mailbox.path
	mailbox.close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("close removed mailbox with an active request: %v", err)
	}
	mailbox.release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("last request lease did not remove mailbox: %v", err)
	}
}
