package shellrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func captureSnapshot[T any](t *testing.T, s *Snapshot, source, destination *T) {
	t.Helper()
	if err := Capture(s, source, destination); err != nil {
		t.Fatal(err)
	}
}
func cloneSnapshot(t *testing.T, s *Snapshot) {
	t.Helper()
	if err := s.Clone(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRebasesActualBindingRoots(t *testing.T) {
	x := 7
	p := &x
	pp := &p
	childX, childP, childPP := x, p, pp
	s := NewSnapshot(nil)
	// Reverse order matters: a pointer is discovered before its scalar root.
	captureSnapshot(t, s, &pp, &childPP)
	captureSnapshot(t, s, &p, &childP)
	captureSnapshot(t, s, &x, &childX)
	cloneSnapshot(t, s)
	if childP != &childX || childPP != &childP {
		t.Fatal("pointer did not reach actual child bindings")
	}
	**childPP = 19
	if childX != 19 || x != 7 {
		t.Fatalf("child=%d parent=%d", childX, x)
	}
	childX = 31
	if *childP != 31 {
		t.Fatal("hidden root copy")
	}
}

type snapshotNode struct {
	N    int
	Next *snapshotNode
	Data map[string]any
}

func TestSnapshotCyclesMapsAndInterfaceIdentity(t *testing.T) {
	node := &snapshotNode{N: 1}
	node.Next = node
	node.Data = map[string]any{"self": node}
	node.Data["map"] = node.Data
	var dynamic any = node
	alias := node.Data
	childNode, childDynamic, childAlias := node, dynamic, alias
	s := NewSnapshot(nil)
	captureSnapshot(t, s, &dynamic, &childDynamic)
	captureSnapshot(t, s, &alias, &childAlias)
	captureSnapshot(t, s, &node, &childNode)
	cloneSnapshot(t, s)
	if childNode == node || childNode.Next != childNode || childDynamic.(*snapshotNode) != childNode || childAlias["self"] != childNode {
		t.Fatal("graph identity was lost")
	}
	childNode.N = 9
	childAlias["new"] = "child"
	if node.N != 1 || node.Data["new"] != nil {
		t.Fatal("parent graph mutated")
	}
	if reflect.ValueOf(childAlias).Pointer() != reflect.ValueOf(childAlias["map"]).Pointer() {
		t.Fatal("map cycle lost")
	}
	var nilPointer *snapshotNode
	var typedNil any = nilPointer
	var cloned any
	s = NewSnapshot(nil)
	captureSnapshot(t, s, &typedNil, &cloned)
	cloneSnapshot(t, s)
	if cloned == nil || reflect.TypeOf(cloned) != reflect.TypeOf(nilPointer) || !reflect.ValueOf(cloned).IsNil() {
		t.Fatal("typed nil identity lost")
	}
}

func TestSnapshotOverlappingSlicesAndArrayRoots(t *testing.T) {
	for _, arrayRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "heap", true: "captured-array"}[arrayRoot], func(t *testing.T) {
			array := [6]int{0, 1, 2, 3, 4, 5}
			a := array[1:3:5]
			b := array[2:5:6]
			p := &array[3]
			childArray, childA, childB, childP := array, a, b, p
			s := NewSnapshot(nil)
			captureSnapshot(t, s, &p, &childP)
			captureSnapshot(t, s, &b, &childB)
			captureSnapshot(t, s, &a, &childA)
			if arrayRoot {
				captureSnapshot(t, s, &array, &childArray)
			}
			cloneSnapshot(t, s)
			if len(childA) != 2 || cap(childA) != 4 || len(childB) != 3 || cap(childB) != 4 {
				t.Fatal("slice shape changed")
			}
			if &childA[1] != &childB[0] || childP != &childB[1] {
				t.Fatal("overlapping storage or element pointer lost")
			}
			*childP = 44
			if childA[:cap(childA)][2] != 44 || array[3] != 3 {
				t.Fatal("slice alias or isolation failed")
			}
			if arrayRoot && childP != &childArray[3] {
				t.Fatal("slice did not use actual child array")
			}
			childA = append(childA, 55)
			if childB[1] != 55 {
				t.Fatal("capacity tail alias lost")
			}
		})
	}
}

func TestSnapshotPointersIntoSliceStructFields(t *testing.T) {
	type item struct {
		N      int
		Nested [2]int
	}
	items := []item{{N: 4, Nested: [2]int{6, 7}}}
	p := &items[0].Nested[1]
	childItems, childP := items, p
	s := NewSnapshot(nil)
	captureSnapshot(t, s, &p, &childP)
	captureSnapshot(t, s, &items, &childItems)
	cloneSnapshot(t, s)
	if childP != &childItems[0].Nested[1] {
		t.Fatal("interior pointer lost")
	}
	*childP = 12
	if items[0].Nested[1] != 7 {
		t.Fatal("parent changed")
	}
}

func TestSnapshotPointerArrayOwnsSliceStorage(t *testing.T) {
	p := &[4]int{1, 2, 3, 4}
	a := p[1:]
	q := &p[2]
	childP, childA, childQ := p, a, q
	s := NewSnapshot(nil)
	captureSnapshot(t, s, &q, &childQ)
	captureSnapshot(t, s, &a, &childA)
	captureSnapshot(t, s, &p, &childP)
	cloneSnapshot(t, s)
	if childQ != &childP[2] || &childA[1] != childQ {
		t.Fatal("pointer-owned array storage split")
	}
}

func TestSnapshotReadonlyRebasesRootsAndAliasedObjects(t *testing.T) {
	x := 8
	p := &x
	values := []int{1, 2, 3, 4}
	marked := values[:2]
	alias := values[1:]
	object := map[string]int{"v": 7}
	other := object
	readonly := &ReadonlyState{}
	for name, binding := range map[string]any{"x": &x, "marked": &marked, "object": &object} {
		if err := readonly.Mark(name, binding); err != nil {
			t.Fatal(err)
		}
	}
	childX, childP, childMarked, childAlias, childObject, childOther := x, p, marked, alias, object, other
	s := NewSnapshot(readonly)
	captureSnapshot(t, s, &x, &childX)
	captureSnapshot(t, s, &p, &childP)
	captureSnapshot(t, s, &marked, &childMarked)
	captureSnapshot(t, s, &alias, &childAlias)
	captureSnapshot(t, s, &object, &childObject)
	captureSnapshot(t, s, &other, &childOther)
	cloneSnapshot(t, s)
	child := s.Readonly()
	if child == readonly || child.CheckAssign(&childX) == nil || child.CheckAssign(&x) != nil {
		t.Fatal("readonly root identities not rebased")
	}
	if child.CheckMutation("p", &childP, childP, "p.*", "field") == nil {
		t.Fatal("readonly scalar through pointer lost")
	}
	if child.CheckBuiltin(&childAlias, childAlias, "clear") == nil || child.CheckBuiltin(&childOther, childOther, "clear") == nil {
		t.Fatal("readonly aliases lost")
	}
	if readonly.CheckBuiltin(&childAlias, childAlias, "clear") != nil {
		t.Fatal("parent readonly state contaminated")
	}
	// Child-only marks do not flow back to the parent state.
	local := []int{9}
	if err := child.Mark("local", &local); err != nil {
		t.Fatal(err)
	}
	if readonly.CheckAssign(&local) != nil {
		t.Fatal("child mark leaked")
	}
}

func TestSnapshotRestrictsChannelAuthorityWithoutTouchingOwner(t *testing.T) {
	owner := &ChannelScope{}
	channel, err := MakeChannel[int](owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	channel <- 7
	childChannel := channel
	noPolicy := NewSnapshot(nil)
	captureSnapshot(t, noPolicy, &channel, &childChannel)
	if noPolicy.Clone() == nil {
		t.Fatal("implicit capability copy accepted")
	}
	shared := NewSnapshot(nil, owner)
	captureSnapshot(t, shared, &channel, &childChannel)
	if shared.Clone() == nil {
		t.Fatal("shared capability accepted")
	}
	child := &ChannelScope{}
	s := NewSnapshot(nil, child)
	captureSnapshot(t, s, &channel, &childChannel)
	cloneSnapshot(t, s)
	if childChannel != channel {
		t.Fatal("opaque handle identity changed")
	}
	if _, _, err := Receive(context.Background(), nil, child, childChannel); !errors.Is(err, ErrForeignChannel) {
		t.Fatalf("child has authority: %v", err)
	}
	if got := <-channel; got != 7 {
		t.Fatal("snapshot consumed channel")
	}
	if err := CloseChannel(owner, channel); err != nil {
		t.Fatal(err)
	}
	child.Close()
	owner.Close()
}

func TestSnapshotRejectsOpaqueResourcesAndCancellation(t *testing.T) {
	for _, value := range []any{func() {}, context.Background()} {
		source := value
		var child any
		s := NewSnapshot(nil)
		captureSnapshot(t, s, &source, &child)
		if err := s.Clone(); err == nil {
			t.Fatalf("opaque %T accepted", value)
		}
	}
	root := &snapshotNode{N: 1}
	root.Next = root
	for i := 0; i < 100; i++ {
		child := root
		s := NewSnapshot(nil)
		captureSnapshot(t, s, &root, &child)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.CloneContext(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if child != root || root.N != 1 {
			t.Fatal("pre-cancel modified bindings")
		}
	}
}

func TestSnapshotNamedMapAliasesAndEmptyValues(t *testing.T) {
	type namedMap map[string]int
	original := map[string]int{"v": 1}
	named := namedMap(original)
	childOriginal, childNamed := original, named
	readonly := &ReadonlyState{}
	if err := readonly.Mark("named", &named); err != nil {
		t.Fatal(err)
	}
	s := NewSnapshot(readonly)
	captureSnapshot(t, s, &original, &childOriginal)
	captureSnapshot(t, s, &named, &childNamed)
	cloneSnapshot(t, s)
	childOriginal["v"] = 9
	if childNamed["v"] != 9 || original["v"] != 1 {
		t.Fatal("named map conversion split identity")
	}
	if s.Readonly().CheckBuiltin(&childNamed, childNamed, "clear") == nil {
		t.Fatal("named readonly map lost")
	}
	empty := &struct{}{}
	var child *struct{}
	s = NewSnapshot(nil)
	captureSnapshot(t, s, &empty, &child)
	cloneSnapshot(t, s)
	if child == nil {
		t.Fatal("non-nil empty pointer lost")
	}
}

func TestSnapshotConvertedPointerAliases(t *testing.T) {
	type Count int
	original := 7
	alias := (*Count)(&original)
	childOriginal, childAlias := original, alias
	s := NewSnapshot(nil)
	captureSnapshot(t, s, &alias, &childAlias)
	captureSnapshot(t, s, &original, &childOriginal)
	cloneSnapshot(t, s)
	*childAlias = 12
	if childOriginal != 12 || original != 7 {
		t.Fatal("converted pointer alias split storage")
	}
}

// This is source-interpreter versus runtime-helper evidence. Compiler dispatch
// and artifact generation are deliberately left to the lower package gate.
func TestSnapshotPublicPointerAndReadonlyOracles(t *testing.T) {
	cases := []struct {
		name, source string
		native       func(*testing.T) (string, string, error)
	}{
		{"pointer", `func main() {
 x := 1
 p := &x
 ( *p = 2; printf 'sub:%s\n' "$x" )
 printf 'parent:%s\n' "$x"
}
main()
`, func(t *testing.T) (string, string, error) {
			x := 1
			p := &x
			childX, childP := x, p
			s := NewSnapshot(nil)
			captureSnapshot(t, s, &x, &childX)
			captureSnapshot(t, s, &p, &childP)
			cloneSnapshot(t, s)
			*childP = 2
			return fmt.Sprintf("sub:%d\nparent:%d\n", childX, x), "", nil
		}},
		{"readonly", `cfg := map[string][]int{"ports": {80, 443}}
readonly cfg
(
 cfg["ports"][0] = 8080
)
`, func(t *testing.T) (string, string, error) {
			cfg := map[string][]int{"ports": {80, 443}}
			readonly := &ReadonlyState{}
			if err := readonly.Mark("cfg", &cfg); err != nil {
				t.Fatal(err)
			}
			child := cfg
			s := NewSnapshot(readonly)
			captureSnapshot(t, s, &cfg, &child)
			cloneSnapshot(t, s)
			err := s.Readonly().CheckMutation("cfg", &child, child["ports"], `["ports"][0]`, "slice")
			if err == nil {
				t.Fatal("readonly guard omitted")
			}
			if cfg["ports"][0] != 80 {
				t.Fatal("parent mutation")
			}
			return "", err.Error() + "\n", err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), "snapshot.bpp")
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			sourceErr := runner.Run(context.Background(), tree)
			gotOut, gotErr, err := tc.native(t)
			if stdout.String() != gotOut || stderr.String() != gotErr || (sourceErr == nil) != (err == nil) {
				t.Fatalf("source=%q/%q/%v native=%q/%q/%v", stdout.String(), stderr.String(), sourceErr, gotOut, gotErr, err)
			}
			if sourceErr != nil && !errors.Is(sourceErr, interp.ExitStatus(2)) {
				t.Fatal(sourceErr)
			}
			if err != nil && err.(interface{ ExitStatus() int }).ExitStatus() != 2 {
				t.Fatal(err)
			}
		})
	}
}

func TestSnapshotParentChildGraphsMutateIndependently(t *testing.T) {
	parent := map[string][]int{"x": {1, 2, 3}}
	child := parent
	s := NewSnapshot(nil)
	captureSnapshot(t, s, &parent, &child)
	cloneSnapshot(t, s)
	var done sync.WaitGroup
	done.Add(2)
	mutate := func(graph map[string][]int) {
		defer done.Done()
		for i := 0; i < 1000; i++ {
			graph["x"][0]++
			graph["last"] = []int{i}
		}
	}
	go mutate(parent)
	go mutate(child)
	done.Wait()
	if parent["x"][0] != 1001 || child["x"][0] != 1001 {
		t.Fatal("parent/child graph shared storage")
	}
}

func TestSnapshotReadonlyDefinedPointerIdentity(t *testing.T) {
	type pointer *int
	x := 4
	p := pointer(&x)
	alias := p
	childP, childAlias := p, alias
	readonly := &ReadonlyState{}
	if err := readonly.Mark("p", &p); err != nil {
		t.Fatal(err)
	}
	s := NewSnapshot(readonly)
	captureSnapshot(t, s, &p, &childP)
	captureSnapshot(t, s, &alias, &childAlias)
	cloneSnapshot(t, s)
	if s.Readonly().CheckMutation("alias", &childAlias, childAlias, "*alias", "field") == nil {
		t.Fatal("defined pointer readonly identity lost")
	}
}
