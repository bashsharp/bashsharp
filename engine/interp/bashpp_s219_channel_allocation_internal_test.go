// Sprint: #219; Story: #463; Story-ID: a6f104b906d9
package interp

import (
	"errors"
	"testing"
)

func TestS219ChannelAllocationResponseClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
		ok   bool
	}{
		{name: "makechan runtime panic", err: errors.New("native dependency panic: makechan: size out of range"), want: "makechan: size out of range", ok: true},
		{name: "unknown type bridge error", err: errors.New("channel make requires one capacity and a registered channel type")},
		{name: "arbitrary worker panic", err: errors.New("native dependency panic: reflect.ChanOf: element size too large")},
		{name: "forged unlabelled runtime text", err: errors.New("makechan: size out of range")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := goSourceNativeMakeChannelPanic(test.err)
			if got != test.want || ok != test.ok {
				t.Fatalf("classification = (%q, %v), want (%q, %v)", got, ok, test.want, test.ok)
			}
		})
	}
}
