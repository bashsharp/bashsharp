package shellrt

import (
	"context"
	"sync"
	"testing"
)

func TestShortFailuresShareSequentialCallsButForkTasks(t *testing.T) {
	p := lexicalProgram(t)
	mark := p.ShortFailureMark()
	entered, err := p.Enter(Site{Name: "nested"}, false)
	if err != nil {
		t.Fatal(err)
	}
	entered.ShortFailure()
	p.SetStatus(0)
	p.SettleShortFailures(mark)
	if p.Status() != 2 {
		t.Fatal(p.Status())
	}
	child := p.Child(context.Background(), p.Session)
	if child.ShortFailureMark() != 0 {
		t.Fatal("task copied parent failure sequence")
	}
	child.ShortFailure()
	if p.ShortFailureMark() != 1 {
		t.Fatal("task changed parent sequence")
	}
	p.SetStatus(0)
	p.SettleShortFailures(p.ShortFailureMark())
	if p.Status() != 0 {
		t.Fatal("unchanged mark overwrote command status")
	}
}
func TestShortFailuresArePerEntryAndRaceSafe(t *testing.T) {
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			p, err := NewProgram()
			if err != nil {
				t.Error(err)
				return
			}
			defer p.Session.Close()
			mark := p.ShortFailureMark()
			p.ShortFailure()
			p.SettleShortFailures(mark)
			if p.Status() != 2 || p.ShortFailureMark() != 1 {
				t.Error("entry failure state was shared")
			}
		}()
	}
	group.Wait()
}
