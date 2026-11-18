// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"encoding/json"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashyCasedEnv is the bridge that carries case-colliding exported names
// across the Windows exec boundary. Shell variable names are case-sensitive
// on every platform (see overlayEnviron.normalize), but two boundaries fold
// them on Windows: os/exec dedupes a child's environment case-insensitively,
// keeping one survivor per folded name, and expand.ListEnviron folds lookups
// the same way on import. A parent that exports both `a` and `A` therefore
// hands a child a coin flip on map iteration order — bash's varenv23.sub
// caught exactly that, with `$a` answering AVAR from an exported A
// (varenv.right:291, runs 35739323028 vs 35758543516).
//
// So when the pairs bound for a child collide under case folding, the full
// case-sensitive set rides along in this variable as a JSON array of
// "name=value" strings. A child shell adopts it at Reset, restoring the
// exact names into its own scope ahead of the folded environment, and hides
// the bridge like the other BASHY_* handoffs. A native child sees one extra
// variable and no behavior change: its environment is case-insensitive
// anyway.
const bashyCasedEnv = "BASHY_CASED_ENV"

// casedEnvMarker reports the JSON payload for [bashyCasedEnv]: every pair
// whose valid shell name collides with a differently-cased name elsewhere
// in pairs. Empty when there is no collision, which is the common case.
func casedEnvMarker(pairs []string) string {
	first := make(map[string]string, len(pairs)) // folded name -> first spelling
	var collided map[string]bool                 // folded names spelled >1 way
	for _, kv := range pairs {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == bashyCasedEnv || !syntax.ValidName(name) {
			continue
		}
		folded := strings.ToUpper(name)
		if seen, ok := first[folded]; !ok {
			first[folded] = name
		} else if seen != name {
			if collided == nil {
				collided = make(map[string]bool)
			}
			collided[folded] = true
		}
	}
	if len(collided) == 0 {
		return ""
	}
	var cased []string
	for _, kv := range pairs {
		name, _, ok := strings.Cut(kv, "=")
		if ok && syntax.ValidName(name) && collided[strings.ToUpper(name)] {
			cased = append(cased, kv)
		}
	}
	b, err := json.Marshal(cased)
	if err != nil {
		return ""
	}
	return string(b)
}

// adoptCasedEnv restores the pairs a parent shell packed into
// [bashyCasedEnv], giving each exported variable back its exact name ahead
// of the case-folded environment the OS delivered.
func (r *Runner) adoptCasedEnv(payload string) {
	var pairs []string
	if json.Unmarshal([]byte(payload), &pairs) != nil {
		return
	}
	for _, kv := range pairs {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !syntax.ValidName(name) {
			continue
		}
		r.setVar(name, expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value})
	}
}
