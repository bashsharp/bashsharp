package shellrt

import (
	"context"
	"testing"
)

func TestDecoratorScopedContinuation(t *testing.T) {
	type key struct{}
	previous := Decorators
	defer func() { Decorators = previous }()
	p, err := NewProgram()
	if err != nil {
		t.Fatal(err)
	}
	original := p.Context
	var seen []string
	Decorators = map[string]DecoratorFunc{
		"guard": func(ctx context.Context, c *Call, args []DecoratorArg) error {
			c.Next(context.WithValue(ctx, key{}, "limited"))
			c.Next()
			return nil
		},
	}
	call := &Call{}
	rungs := []Decorator{{Name: "guard"}, {Run: func(region *Program, c *Call) {
		if region.Context.Value(key{}) != nil {
			seen = append(seen, "script limited")
		} else {
			seen = append(seen, "script original")
		}
		c.Next()
	}}}
	if !p.Decorate(call, rungs, func(region *Program) error {
		if region.Context.Value(key{}) != nil {
			seen = append(seen, "body limited")
		} else {
			seen = append(seen, "body original")
		}
		return nil
	}) {
		t.Fatal("chain failed")
	}
	want := []string{"script limited", "body limited", "script original", "body original"}
	if len(seen) != len(want) {
		t.Fatal(seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("got %v want %v", seen, want)
		}
	}
	if p.Context != original || p.Context.Value(key{}) != nil {
		t.Fatal("context escaped continuation")
	}
}
