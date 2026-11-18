package shellrt

import (
	"context"
	"testing"
)

func TestTaskContextFollowsNativeOwnershipAndCleanup(t *testing.T) {
	session, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if InTask(session.Context()) {
		t.Fatal("owner marked as task")
	}
	task := session.Go(func(ctx context.Context, child *Session) error {
		if !InTask(ctx) || !InTask(child.Context()) || !InTask(context.WithoutCancel(child.Context())) {
			t.Error("task policy marker lost")
		}
		nested := child.Go(func(ctx context.Context, nested *Session) error {
			if !InTask(ctx) || !InTask(nested.Context()) {
				t.Error("descendant policy marker lost")
			}
			return nil
		})
		return nested.Wait()
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if InTask(session.Context()) {
		t.Fatal("task marker escaped to owner")
	}
}
