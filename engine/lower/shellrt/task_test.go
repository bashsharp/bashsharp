package shellrt_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestTaskSnapshotIsolation(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	s.Set("shared", shellrt.Var{Str: "parent"})
	s.SetStatus(5)

	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		if v, _ := child.Get("shared"); v.Str != "parent" {
			return errors.New("child did not inherit the launch snapshot")
		}
		if child.Status() != 5 {
			return errors.New("child did not inherit the status")
		}
		child.Set("shared", shellrt.Var{Str: "child"})
		child.Set("childonly", shellrt.Var{Str: "1"})
		child.SetStatus(9)
		return nil
	})
	// A write after launch is invisible to the already-running task, and the
	// task's writes are invisible here.
	s.Set("afterlaunch", shellrt.Var{Str: "1"})

	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("shared"); v.Str != "parent" {
		t.Fatalf("parent shared = %q, want parent", v.Str)
	}
	if _, ok := s.Get("childonly"); ok {
		t.Fatal("child writes must not reach the parent")
	}
	if got := s.Status(); got != 5 {
		t.Fatalf("parent Status() = %d, want 5", got)
	}
	if v, _ := task.Session().Get("shared"); v.Str != "child" {
		t.Fatalf("child shared = %q, want child", v.Str)
	}
	if _, ok := task.Session().Get("afterlaunch"); ok {
		t.Fatal("a post-launch parent write must not reach the task snapshot")
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLaunchHandshakeOrdersOrdinals(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	var order []uint64
	var mu sync.Mutex
	release := make(chan struct{})

	for range 5 {
		s.Go(func(ctx context.Context, child *shellrt.Session) error {
			mu.Lock()
			order = append(order, uint64(len(order)))
			mu.Unlock()
			// Committed to a blocking wait: arm, then block.
			child.Arm()
			<-release
			return nil
		})
	}
	// Every task recorded itself before its launcher returned, so all five
	// bodies started even though none has finished.
	mu.Lock()
	got := len(order)
	mu.Unlock()
	if got != 5 {
		t.Fatalf("%d tasks started before join, want 5", got)
	}
	if active := s.Active(); active != 5 {
		t.Fatalf("Active() = %d, want 5", active)
	}
	close(release)
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("Active() = %d after Join, want 0", active)
	}
}

var errBoom = errors.New("boom")

func TestTaskPrimaryFailureIsLowestOrdinal(t *testing.T) {
	t.Parallel()
	// Every task fails outright, so the primary failure is purely a matter of
	// launch order and must not depend on which goroutine ran first.
	for range 20 {
		s, _ := newSession(t)
		start := make(chan struct{})
		for i := range 4 {
			s.Go(func(ctx context.Context, child *shellrt.Session) error {
				child.Arm()
				<-start
				return errors.New("failure " + string(rune('a'+i)))
			})
		}
		close(start)
		err := s.Join()
		var terr *shellrt.TaskError
		if !errors.As(err, &terr) {
			t.Fatalf("err = %v, want a *TaskError", err)
		}
		if terr.Ordinal != 0 || terr.Err.Error() != "failure a" {
			t.Fatalf("primary failure = %+v, want ordinal 0 / failure a", terr)
		}
	}
}

func TestTaskPrimaryFailurePrefersGenuineOverCancellation(t *testing.T) {
	t.Parallel()
	// Task 0 blocks and is only cancelled because task 2 failed. Reporting
	// task 0's context.Canceled would name the victim, not the cause.
	for range 20 {
		s, _ := newSession(t)
		s.Go(func(ctx context.Context, child *shellrt.Session) error {
			child.Arm()
			<-ctx.Done()
			return ctx.Err()
		})
		s.Go(func(ctx context.Context, child *shellrt.Session) error {
			child.Arm()
			<-ctx.Done()
			return ctx.Err()
		})
		s.Go(func(ctx context.Context, child *shellrt.Session) error {
			return errBoom
		})
		err := s.Join()
		var terr *shellrt.TaskError
		if !errors.As(err, &terr) {
			t.Fatalf("err = %v, want a *TaskError", err)
		}
		if terr.Ordinal != 2 || !errors.Is(err, errBoom) {
			t.Fatalf("primary failure = %+v, want ordinal 2 / boom", terr)
		}
	}
}

func TestTaskFailureCancelsSiblings(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	var canceled atomic.Bool
	blocked := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		child.Arm()
		<-ctx.Done()
		canceled.Store(true)
		return nil
	})
	s.Go(func(ctx context.Context, child *shellrt.Session) error { return errBoom })

	// Join returns only once the blocked sibling has observed cancellation.
	err := s.Join()
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if err := blocked.Wait(); err != nil {
		t.Fatal(err)
	}
	if !canceled.Load() {
		t.Fatal("the sibling was not cancelled by the failure")
	}
}

func TestSessionCancelUnblocksTasks(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	for range 3 {
		s.Go(func(ctx context.Context, child *shellrt.Session) error {
			child.Arm()
			<-ctx.Done()
			return ctx.Err()
		})
	}
	s.Cancel()
	err := s.Join()
	var terr *shellrt.TaskError
	if !errors.As(err, &terr) {
		t.Fatalf("err = %v, want a *TaskError", err)
	}
	// All three failures are cancellations, so the lowest ordinal wins.
	if terr.Ordinal != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("primary failure = %+v, want ordinal 0 / context.Canceled", terr)
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("Active() = %d after Join, want 0", active)
	}
}

func TestSessionCloseJoinsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	done := make(chan struct{})
	s.Go(func(ctx context.Context, child *shellrt.Session) error {
		child.Arm()
		<-ctx.Done()
		close(done)
		return nil
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Close returned before the task finished")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("Active() = %d after Close, want 0", active)
	}
}

func TestNestedTasksAreReapedByTheirOwner(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	var nested atomic.Int64
	inner := make(chan struct{})

	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		for range 3 {
			child.Go(func(ctx context.Context, grand *shellrt.Session) error {
				grand.Arm()
				<-inner
				nested.Add(1)
				return nil
			})
		}
		// The body returns while its own tasks are still running; the runtime
		// must join them before the parent sees this task as finished.
		close(inner)
		return nil
	})
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if got := nested.Load(); got != 3 {
		t.Fatalf("%d nested tasks finished, want 3", got)
	}
	if active := task.Session().Active(); active != 0 {
		t.Fatalf("child Active() = %d, want 0", active)
	}
}

func TestNestedTaskFailureSurfacesThroughItsOwner(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	s.Go(func(ctx context.Context, child *shellrt.Session) error {
		child.Go(func(ctx context.Context, grand *shellrt.Session) error { return errBoom })
		return nil
	})
	err := s.Join()
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestGoAfterJoinDoesNotLeak(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Bool
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		ran.Store(true)
		return nil
	})
	if err := task.Wait(); !errors.Is(err, shellrt.ErrSessionClosed) {
		t.Fatalf("err = %v, want ErrSessionClosed", err)
	}
	if ran.Load() {
		t.Fatal("a task must not start after the owning session was joined")
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("Active() = %d, want 0", active)
	}
}

func TestTaskPanicBecomesAFailureAndStillJoins(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	var siblingCanceled atomic.Bool
	sibling := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		child.Arm()
		<-ctx.Done()
		siblingCanceled.Store(true)
		return nil
	})
	panicked := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		panic("nope")
	})

	// Structured cancellation and join both run: the panic is a task failure,
	// not a process crash, so the group is cancelled and Join still returns.
	err := s.Join()
	var terr *shellrt.TaskError
	if !errors.As(err, &terr) {
		t.Fatalf("Join() = %v, want a *TaskError", err)
	}
	var tp *shellrt.TaskPanic
	if !errors.As(err, &tp) {
		t.Fatalf("Join() = %v, want a *TaskPanic", err)
	}
	if tp.Value != "nope" || tp.Ordinal != 1 {
		t.Fatalf("panic = %+v, want ordinal 1 / nope", tp)
	}
	if len(tp.Stack) == 0 {
		t.Fatal("the panic failure carries no stack")
	}
	if err := panicked.Wait(); !errors.As(err, &tp) {
		t.Fatalf("task err = %v, want a *TaskPanic", err)
	}
	if err := sibling.Wait(); err != nil {
		t.Fatal(err)
	}
	if !siblingCanceled.Load() {
		t.Fatal("the panic did not cancel the sibling")
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("Active() = %d after Join, want 0", active)
	}
}

func TestPanickingTaskStillReapsItsOwnTasks(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	var nested atomic.Int64
	s.Go(func(ctx context.Context, child *shellrt.Session) error {
		for range 3 {
			child.Go(func(ctx context.Context, grand *shellrt.Session) error {
				grand.Arm()
				<-ctx.Done()
				nested.Add(1)
				return nil
			})
		}
		panic("boom in the middle")
	})
	var tp *shellrt.TaskPanic
	if err := s.Join(); !errors.As(err, &tp) {
		t.Fatalf("Join() = %v, want a *TaskPanic", err)
	}
	// child.Close ran on the panic path, so no nested task leaked.
	if got := nested.Load(); got != 3 {
		t.Fatalf("%d nested tasks were reaped, want 3", got)
	}
}

func TestLegacyShellRegionArmsTheLaunchHandshake(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithShellFactory(legacyShellFactory))
	release := make(chan struct{})
	// Legacy backends cannot expose semantic suspension points. Entering their
	// shell region retains the original conservative arming contract.
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		if err := child.Shell(ctx, "echo in-task"); err != nil {
			return err
		}
		<-release
		return nil
	})
	// Reaching here at all is the assertion: Go did not wait for the body.
	close(release)
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "in-task\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestBlockingArmsTheLaunchHandshake(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	release := make(chan struct{})
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		return child.Blocking(func() error {
			<-release
			return nil
		})
	})
	close(release)
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationReleasesAnUnarmedLauncher(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	release := make(chan struct{})
	defer close(release)
	// A body that violates the arming obligation: it blocks on a channel the
	// runtime knows nothing about and never arms. Cancelling the group must
	// still release the launcher, so a buggy body cannot wedge the program's
	// shutdown path.
	launched := make(chan struct{})
	go func() {
		s.Go(func(ctx context.Context, child *shellrt.Session) error {
			<-release
			return nil
		})
		close(launched)
	}()
	select {
	case <-launched:
		t.Fatal("Go returned before the body armed or the group was cancelled")
	case <-time.After(20 * time.Millisecond):
	}
	s.Cancel()
	<-launched
}

func TestConcurrentLaunchAndJoinIsRaceFree(t *testing.T) {
	t.Parallel()
	// Exercises the group's bookkeeping from several goroutines at once, so
	// the race detector sees launch, finish, join and close interleaved.
	s, _ := newSession(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
				child.Set("n", shellrt.Var{Str: "1"})
				return nil
			})
			task.Wait()
			s.Active()
		}()
	}
	wg.Wait()
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
