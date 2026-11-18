package interp

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831

import "testing"

// TestTestingCallbackStatusProvenance pins the exact condition under which a
// returned test callback's status 1 is reduced to a pass.
//
// The reduction exists for one measured shape: a body whose last act is
// `defer func() { recover() }()`, where status 1 is recover's own answer
// ("nothing to recover") rather than a failure. Stating that as "status 1
// never fails a callback" would be a blanket that reduces any native or
// runtime failure reporting 1 to a pass.
//
// Most cases below hold the status at 1 and vary only the provenance, which is
// the whole of what a blanket form cannot see: it reduces every one of them to
// 0, so "unstamped status one", "stamp superseded within the callback" and
// "stamp issued before this callback" fail against a blanket condition and
// pass only against the provenance-keyed one. This is the discriminating
// evidence for the narrowing; the end-to-end shapes in
// [TestGoSourceTestingRecoverProvenance] pin the same rule through the real
// scheduler but do not by themselves distinguish the two forms, because their
// failures are already carried by exit.err or by a status other than 1.
func TestTestingCallbackStatusProvenance(t *testing.T) {
	for _, tc := range []struct {
		name string
		// runnerSeq is bashPPRecoverSeq after the callback returned.
		runnerSeq uint64
		// mark is bashPPRecoverSeq sampled before the callback ran.
		mark uint64
		exit exitStatus
		want uint8
	}{{
		// The shape the reduction is for: recover reported during this
		// callback and its answer is the status still standing.
		name:      "recover answer from this callback",
		runnerSeq: 1, mark: 0,
		exit: exitStatus{code: 1, recoverSeq: 1},
		want: 0,
	}, {
		// No stamp at all. A status 1 nobody claims is a real failure.
		name:      "unstamped status one",
		runnerSeq: 0, mark: 0,
		exit: exitStatus{code: 1},
		want: 1,
	}, {
		// recover reported, then something else reported after it. The
		// standing status is not recover's answer even though a recover ran.
		name:      "stamp superseded within the callback",
		runnerSeq: 2, mark: 0,
		exit: exitStatus{code: 1, recoverSeq: 1},
		want: 1,
	}, {
		// The stamp predates this callback: an earlier test recovered and this
		// one merely failed. One callback must not excuse another.
		name:      "stamp issued before this callback",
		runnerSeq: 1, mark: 1,
		exit: exitStatus{code: 1, recoverSeq: 1},
		want: 1,
	}, {
		// Terminating conditions outrank the stamp.
		name:      "explicit exit despite stamp",
		runnerSeq: 1, mark: 0,
		exit: exitStatus{code: 1, recoverSeq: 1, exiting: true},
		want: 1,
	}, {
		name:      "fatal exit despite stamp",
		runnerSeq: 1, mark: 0,
		exit: exitStatus{code: 1, recoverSeq: 1, fatalExit: true, exiting: true},
		want: 1,
	}, {
		// A bridge or interpreter diagnostic reports bashPPPanicStatus and is
		// never a candidate for the reduction.
		name:      "diagnostic status is untouched",
		runnerSeq: 1, mark: 0,
		exit: exitStatus{code: 2, recoverSeq: 1},
		want: 2,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{bashPPRecoverSeq: tc.runnerSeq, exit: tc.exit}
			if got := r.testingCallbackStatus(tc.mark); got != tc.want {
				t.Fatalf("testingCallbackStatus(%d) = %d, want %d (exit %+v, seq %d)",
					tc.mark, got, tc.want, tc.exit, tc.runnerSeq)
			}
		})
	}
}

// TestTestingCallbackStatusPanicStillFails pins that a panic unwinding out of
// the body fails its callback even when a recover stamped the runner first --
// the `recover(); t.Log(); panic("deliberate")` shape.
func TestTestingCallbackStatusPanicStillFails(t *testing.T) {
	r := &Runner{bashPPRecoverSeq: 1, exit: exitStatus{code: 1, recoverSeq: 1}}
	r.bashPPPanic.active = true
	if got := r.testingCallbackStatus(0); got != 1 {
		t.Fatalf("panicking callback reduced to %d, want 1", got)
	}
}
