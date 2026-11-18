package shellrt

import (
	"context"
	"errors"
	"reflect"
	"sort"
)

// Snapshot copies a certified native value graph into actual child binding
// locations. Capture every root before Clone: pointers into another root must
// point to the generated child's binding, not a hidden temporary copy.
// Snapshot is synchronous and must not race with mutation of its source graph.
type Snapshot struct {
	parent    *ReadonlyState
	readonly  *ReadonlyState
	channels  *ChannelScope
	roots     []snapshotRoot
	addresses map[snapshotKey]reflect.Value
	pointers  []reflect.Value
	slices    []reflect.Value
	regions   []*snapshotRegion
	seen      map[snapshotVisit]bool
	maps      map[snapshotKey]reflect.Value
	ctx       context.Context
	started   bool
}
type snapshotRoot struct{ source, destination reflect.Value }
type snapshotKey struct {
	typ     reflect.Type
	address uintptr
}
type snapshotVisit struct {
	snapshotKey
	kind     reflect.Kind
	capacity int
}
type snapshotRegion struct {
	elem         reflect.Type
	first, limit uintptr
	sources      []reflect.Value
	target       reflect.Value
}

// SnapshotError rejects values whose ownership cannot safely cross this
// boundary. It is a runtime boundary error, never a language panic.
type SnapshotError struct{ Message string }

func (e *SnapshotError) Error() string { return "bash++: snapshot: " + e.Message }
func (*SnapshotError) ExitStatus() int { return 2 }

// NewSnapshot optionally accepts the child's channel scope. Channel handles
// retain identity, but this scope must not own them; copying channel authority
// is forbidden. Without an explicit scope, non-nil channels are rejected.
func NewSnapshot(parent *ReadonlyState, childChannels ...*ChannelScope) *Snapshot {
	s := &Snapshot{parent: parent, readonly: &ReadonlyState{}, addresses: map[snapshotKey]reflect.Value{}, seen: map[snapshotVisit]bool{}, maps: map[snapshotKey]reflect.Value{}}
	if len(childChannels) == 1 {
		s.channels = childChannels[0]
	}
	return s
}

// Capture registers actual source and child binding addresses. The compiler
// must save all source addresses before introducing shadows. Capture never
// mutates either binding; Clone must succeed before the child body executes.
func Capture[T any](s *Snapshot, source, destination *T) error {
	if s == nil || s.started {
		return &SnapshotError{"capture after cloning started"}
	}
	if source == nil || destination == nil || source == destination {
		return &SnapshotError{"capture requires distinct non-nil binding addresses"}
	}
	s.roots = append(s.roots, snapshotRoot{reflect.ValueOf(source).Elem(), reflect.ValueOf(destination).Elem()})
	return nil
}

func (s *Snapshot) Readonly() *ReadonlyState { return s.readonly }
func (s *Snapshot) Clone() error             { return s.CloneContext(context.Background()) }

// CloneContext supports cancellation without creating workers or acquiring
// foreign resources. Destinations must not be used after an error; there is no
// partial child execution. Successful cloning leaves the parent graph intact.
func (s *Snapshot) CloneContext(ctx context.Context) error {
	if s.started {
		return &SnapshotError{"snapshot can be cloned only once"}
	}
	s.started = true
	s.ctx = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, root := range s.roots {
		if err := s.register(root.source, root.destination); err != nil {
			return err
		}
	}
	for _, root := range s.roots {
		if err := s.discover(root.source); err != nil {
			return err
		}
	}
	s.planRegions()
	sort.SliceStable(s.pointers, func(i, j int) bool { return s.pointers[i].Type().Elem().Size() > s.pointers[j].Type().Elem().Size() })
	// Allocate ordinary pointees before slices so pointers to arrays/structs can
	// supply the slice's real backing storage. Slice elements are allocated by
	// their shared region, independent of root traversal order.
	for _, p := range s.pointers {
		k := snapshotKey{p.Type().Elem(), p.Pointer()}
		if _, ok := s.addresses[k]; ok || s.inSlice(k) {
			continue
		}
		destination := reflect.New(p.Type().Elem()).Elem()
		for existing, value := range s.addresses {
			if existing.address == k.address && value.Addr().Type().ConvertibleTo(p.Type()) {
				destination = value.Addr().Convert(p.Type()).Elem()
				break
			}
		}
		if err := s.register(p.Elem(), destination); err != nil {
			return err
		}
	}
	for _, r := range s.regions {
		if err := s.allocateRegion(r); err != nil {
			return err
		}
	}
	for _, p := range s.pointers {
		if _, ok := s.addresses[snapshotKey{p.Type().Elem(), p.Pointer()}]; !ok && p.Type().Elem().Size() > 0 {
			return &SnapshotError{"unmapped pointer into slice storage"}
		}
	}
	// Memoized maps and pointers are populated once. Root values are committed
	// last, after all graph nodes exist, preserving cycles through root cells.
	filled := map[snapshotKey]bool{}
	for _, p := range s.pointers {
		k := snapshotKey{p.Type().Elem(), p.Pointer()}
		if filled[k] || k.typ.Size() == 0 {
			continue
		}
		filled[k] = true
		value, err := s.copyValue(p.Elem())
		if err != nil {
			return err
		}
		s.addresses[k].Set(value)
	}
	for _, r := range s.regions {
		for _, source := range r.sources {
			full := source.Slice(0, source.Cap())
			offset := int((source.Pointer() - r.first) / r.elem.Size())
			for i := 0; i < full.Len(); i++ {
				value, err := s.copyValue(full.Index(i))
				if err != nil {
					return err
				}
				r.target.Index(offset + i).Set(value)
			}
		}
	}
	values := make([]reflect.Value, len(s.roots))
	for i, root := range s.roots {
		value, err := s.copyValue(root.source)
		if err != nil {
			return err
		}
		values[i] = value
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, root := range s.roots {
		root.destination.Set(values[i])
	}
	s.copyReadonly()
	return nil
}

func (s *Snapshot) register(source, destination reflect.Value) error {
	if source.CanAddr() && source.Type().Size() > 0 {
		k := snapshotKey{source.Type(), source.Addr().Pointer()}
		if old, exists := s.addresses[k]; exists && old.Addr().Pointer() != destination.Addr().Pointer() {
			return &SnapshotError{"overlapping roots require the same child location"}
		}
		s.addresses[k] = destination
	}
	switch source.Kind() {
	case reflect.Struct:
		for i := 0; i < source.NumField(); i++ {
			if source.Type().Field(i).PkgPath != "" {
				return &SnapshotError{"opaque field in " + source.Type().String()}
			}
			if err := s.register(source.Field(i), destination.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Array:
		for i := 0; i < source.Len(); i++ {
			if err := s.register(source.Index(i), destination.Index(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Snapshot) discover(v reflect.Value) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			return s.discover(v.Elem())
		}
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return nil
		}
		capacity := 0
		if v.Kind() == reflect.Slice {
			capacity = v.Cap()
		}
		visit := snapshotVisit{snapshotKey{v.Type(), v.Pointer()}, v.Kind(), capacity}
		if s.seen[visit] {
			return nil
		}
		s.seen[visit] = true
		switch v.Kind() {
		case reflect.Pointer:
			s.pointers = append(s.pointers, v)
			return s.discover(v.Elem())
		case reflect.Map:
			key := snapshotKey{reflect.MapOf(v.Type().Key(), v.Type().Elem()), v.Pointer()}
			if _, exists := s.maps[key]; !exists {
				s.maps[key] = reflect.MakeMapWithSize(key.typ, v.Len())
			}
			iter := v.MapRange()
			for iter.Next() {
				if err := s.discover(iter.Key()); err != nil {
					return err
				}
				if err := s.discover(iter.Value()); err != nil {
					return err
				}
			}
		case reflect.Slice:
			s.slices = append(s.slices, v)
			full := v.Slice(0, v.Cap())
			for i := 0; i < full.Len(); i++ {
				if err := s.discover(full.Index(i)); err != nil {
					return err
				}
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				return &SnapshotError{"opaque field in " + v.Type().String()}
			}
			if err := s.discover(v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := s.discover(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Chan:
		if v.IsNil() {
			return nil
		}
		if s.channels == nil {
			return &SnapshotError{"channel handle requires an explicit restricted child scope"}
		}
		_, err := s.channels.state(v)
		if !errors.Is(err, ErrForeignChannel) && !errors.Is(err, ErrChannelScopeClosed) {
			return &SnapshotError{"channel authority cannot cross a shell-copy boundary"}
		}
	case reflect.Func:
		if !v.IsNil() {
			return &SnapshotError{"function captures require compiler-managed rebinding"}
		}
	case reflect.UnsafePointer:
		return &SnapshotError{"unsafe pointers cannot cross a shell-copy boundary"}
	}
	return nil
}

func (s *Snapshot) planRegions() {
	for _, v := range s.slices {
		if v.Cap() == 0 || v.Type().Elem().Size() == 0 {
			continue
		}
		first := v.Pointer()
		limit := first + uintptr(v.Cap())*v.Type().Elem().Size()
		s.regions = append(s.regions, &snapshotRegion{elem: v.Type().Elem(), first: first, limit: limit, sources: []reflect.Value{v}})
	}
	sort.Slice(s.regions, func(i, j int) bool { return s.regions[i].first < s.regions[j].first })
	var merged []*snapshotRegion
	for _, r := range s.regions {
		var target *snapshotRegion
		for _, prior := range merged {
			if prior.elem == r.elem && r.first < prior.limit && prior.first < r.limit {
				target = prior
				break
			}
		}
		if target == nil {
			merged = append(merged, r)
		} else {
			if r.limit > target.limit {
				target.limit = r.limit
			}
			target.sources = append(target.sources, r.sources...)
		}
	}
	s.regions = merged
}
func (s *Snapshot) inSlice(k snapshotKey) bool {
	for _, r := range s.regions {
		if k.address >= r.first && k.address+k.typ.Size() <= r.limit && (k.typ == r.elem || k.typ.Size() < r.elem.Size()) {
			return true
		}
	}
	return false
}
func (s *Snapshot) allocateRegion(r *snapshotRegion) error {
	count := int((r.limit - r.first) / r.elem.Size())
	// A captured/pointer-owned array takes precedence over fresh allocation.
	for key, destination := range s.addresses {
		if key.typ.Kind() != reflect.Array || key.typ.Elem() != r.elem {
			continue
		}
		if key.address <= r.first && key.address+key.typ.Size() >= r.limit {
			offset := int((r.first - key.address) / r.elem.Size())
			r.target = destination.Slice(offset, offset+count)
			break
		}
	}
	if !r.target.IsValid() {
		r.target = reflect.MakeSlice(reflect.SliceOf(r.elem), count, count)
	}
	for _, source := range r.sources {
		full := source.Slice(0, source.Cap())
		offset := int((source.Pointer() - r.first) / r.elem.Size())
		for i := 0; i < full.Len(); i++ {
			if err := s.register(full.Index(i), r.target.Index(offset+i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Snapshot) copyValue(v reflect.Value) (reflect.Value, error) {
	if err := s.ctx.Err(); err != nil {
		return reflect.Value{}, err
	}
	switch v.Kind() {
	case reflect.Interface:
		result := reflect.New(v.Type()).Elem()
		if !v.IsNil() {
			child, err := s.copyValue(v.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			result.Set(child)
		}
		return result, nil
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		destination := s.addresses[snapshotKey{v.Type().Elem(), v.Pointer()}]
		if !destination.IsValid() { // Zero-sized pointees have no stable identity.
			return reflect.New(v.Type().Elem()).Convert(v.Type()), nil
		}
		return destination.Addr().Convert(v.Type()), nil
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		key := snapshotKey{reflect.MapOf(v.Type().Key(), v.Type().Elem()), v.Pointer()}
		result := s.maps[key]
		// Remove from pending traversal before following a cyclic interface/map.
		visit := snapshotVisit{key, reflect.Invalid, -1}
		if !s.seen[visit] {
			s.seen[visit] = true
			iter := v.MapRange()
			for iter.Next() {
				key, err := s.copyValue(iter.Key())
				if err != nil {
					return reflect.Value{}, err
				}
				value, err := s.copyValue(iter.Value())
				if err != nil {
					return reflect.Value{}, err
				}
				result.SetMapIndex(key, value)
			}
		}
		return result.Convert(v.Type()), nil
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		if v.Cap() == 0 || v.Type().Elem().Size() == 0 {
			return reflect.MakeSlice(v.Type(), v.Len(), v.Cap()), nil
		}
		for _, r := range s.regions {
			if r.elem == v.Type().Elem() && v.Pointer() >= r.first && v.Pointer()+uintptr(v.Cap())*r.elem.Size() <= r.limit {
				offset := int((v.Pointer() - r.first) / r.elem.Size())
				return r.target.Slice3(offset, offset+v.Len(), offset+v.Cap()).Convert(v.Type()), nil
			}
		}
		return reflect.Value{}, &SnapshotError{"unmapped slice storage"}
	case reflect.Struct, reflect.Array:
		result := reflect.New(v.Type()).Elem()
		if v.Kind() == reflect.Struct {
			for i := 0; i < v.NumField(); i++ {
				child, err := s.copyValue(v.Field(i))
				if err != nil {
					return reflect.Value{}, err
				}
				result.Field(i).Set(child)
			}
		} else {
			for i := 0; i < v.Len(); i++ {
				child, err := s.copyValue(v.Index(i))
				if err != nil {
					return reflect.Value{}, err
				}
				result.Index(i).Set(child)
			}
		}
		return result, nil
	default:
		return v, nil
	}
}
func (s *Snapshot) copyReadonly() {
	if s.parent == nil {
		return
	}
	p := s.parent
	p.mu.RLock()
	defer p.mu.RUnlock()
	child := s.readonly
	child.roots = map[readonlyKey]string{}
	child.objects = map[readonlyKey]string{}
	for source, destination := range s.addresses {
		old := readonlyKey{reflect.PointerTo(source.typ), source.address}
		next := readonlyKey{destination.Addr().Type(), destination.Addr().Pointer()}
		if owner := p.roots[old]; owner != "" {
			child.roots[next] = owner
		}
		if owner := p.objects[old]; owner != "" {
			child.objects[next] = owner
		}
		child.retained = append(child.retained, destination.Addr().Interface())
	}
	// A defined pointer type may share a pointee with its unnamed pointer
	// conversion while retaining a distinct readonly identity key.
	for old, owner := range p.objects {
		if old.typ.Kind() != reflect.Pointer {
			continue
		}
		if destination, ok := s.addresses[snapshotKey{old.typ.Elem(), old.address}]; ok {
			child.objects[readonlyKey{old.typ, destination.Addr().Pointer()}] = owner
		}
	}
	for source, destination := range s.maps {
		for old, owner := range p.objects {
			if old.address == source.address && old.typ.Kind() == reflect.Map && old.typ.Key() == source.typ.Key() && old.typ.Elem() == source.typ.Elem() {
				child.objects[readonlyKey{old.typ, destination.Pointer()}] = owner
			}
		}
		child.retained = append(child.retained, destination.Interface())
	}
	for _, region := range p.slices {
		for _, r := range s.regions {
			if region.typ.Elem() != r.elem {
				continue
			}
			first, limit := region.first, region.limit
			if first < r.first {
				first = r.first
			}
			if limit > r.limit {
				limit = r.limit
			}
			if first >= limit {
				continue
			}
			next := r.target.Pointer() + first - r.first
			child.slices = append(child.slices, readonlySlice{region.typ, next, next + limit - first, region.owner})
			child.retained = append(child.retained, r.target.Interface())
		}
	}
}
