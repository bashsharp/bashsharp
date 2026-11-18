//go:build unix

package interp

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBashPPNativeControlListener(t *testing.T) {
	listener, network, cleanup, err := bashPPNativeControlListener()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer listener.Close()
	if network != "unix" && network != "tcp" {
		t.Fatalf("network = %q, want unix or loopback TCP fallback", network)
	}
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = conn.Close()
		}
		accepted <- err
	}()
	conn, err := net.Dial(network, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}

func TestBashPPNativeControlListenerLongTempFallback(t *testing.T) {
	root := t.TempDir()
	longTemp := root
	for len(filepath.Join(longTemp, "bashpp-control-1234567890", "s")) < 180 {
		longTemp = filepath.Join(longTemp, strings.Repeat("x", 30))
		if err := os.Mkdir(longTemp, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", longTemp)
	listener, network, cleanup, err := bashPPNativeControlListener()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer listener.Close()
	if network != "tcp" {
		t.Fatalf("network = %q, want tcp fallback for long Unix socket path", network)
	}
	if host, _, err := net.SplitHostPort(listener.Addr().String()); err != nil || host != "127.0.0.1" {
		t.Fatalf("fallback address = %q: host=%q err=%v", listener.Addr(), host, err)
	}
	entries, err := os.ReadDir(longTemp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed Unix listener left temporary state: %v", entries)
	}
}

func TestBashPPCallbackMailboxRoundTripAndCloseLease(t *testing.T) {
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
	mailbox.answer(slot, bashPPBridgeRequest{ID: got.ID, Op: "callback-reply", Values: []bashPPBridgeValue{{Kind: "int", Text: "7"}}})
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
