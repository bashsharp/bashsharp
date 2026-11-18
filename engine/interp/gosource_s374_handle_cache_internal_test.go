//go:build full

package interp

import "testing"

func TestS374NativeHandleTypeCacheBounded(t *testing.T) {
	s := &bashPPNativeSession{id: "s"}
	const limit = 4096
	first := bashPPBridgeValue{Kind: "handle", Session: "s", Handle: 1, NativeTypeID: 7}
	s.rememberNativeHandleType(first)
	for batch := 0; batch < 2; batch++ {
		for i := 0; i < limit; i++ {
			s.rememberNativeHandleType(bashPPBridgeValue{Kind: "handle", Session: "s", Handle: uint64(2 + batch*limit + i), NativeTypeID: 7})
		}
		t.Logf("handles seen=%d cached=%d", 1+(batch+1)*limit, len(s.handleTypes))
		if len(s.handleTypes) > limit {
			t.Fatalf("type cache retains %d entries, limit %d", len(s.handleTypes), limit)
		}
	}
	// Eviction is a cache miss, never authentication of claimed type metadata.
	first.NativeTypeID = 999
	if _, ok := s.nativeAdmissionKey(first, "any"); ok {
		t.Fatal("evicted identity must use the live type check")
	}
	latest := bashPPBridgeValue{Kind: "handle", Session: "s", Handle: 8193, NativeTypeID: 999}
	if key, ok := s.nativeAdmissionKey(latest, "any"); !ok || key.source != 7 {
		t.Fatalf("latest authenticated identity = %+v, %v", key, ok)
	}
	// Refreshing one handle must not increase the cache cardinality.
	latest.NativeTypeID = 7
	for i := 0; i < limit; i++ {
		s.rememberNativeHandleType(latest)
	}
	if len(s.handleTypes) != 1 {
		t.Fatalf("refresh grew cache to %d", len(s.handleTypes))
	}
}
