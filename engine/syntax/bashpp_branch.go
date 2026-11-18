// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

type bashppControlKind uint8

const (
	bashppControlFor bashppControlKind = iota + 1
	bashppControlRange
	bashppControlSwitch
	bashppControlSelect
)

// bashppBranch reclassifies exactly one bare, unlabeled branch word. Arguments,
// assignments, redirects, and words outside a committed typed control region
// retain their shell meaning.
func (p *Parser) bashppBranch(ce *CallExpr, redirs []*Redirect) *BashPPBranch {
	if len(p.bashppControls) == 0 || ce == nil || len(ce.Assigns) != 0 || len(ce.Args) != 1 || len(redirs) != 0 {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	if kw == nil {
		return nil
	}
	switch kw.Value {
	case "break", "continue", "fallthrough":
		return &BashPPBranch{Kw: kw}
	}
	return nil
}

// bashppValidateBranches validates the complete outer typed-control tree only
// after its transactional parser has committed. This prevents a deterministic
// typed diagnostic from being lost when an inner switch/select recognizer
// rewinds a classic-shell near miss.
func (p *Parser) bashppValidateBranches(root Command) {
	allowedFallthrough := make(map[*BashPPBranch]bool)
	finalFallthrough := make(map[*BashPPBranch]bool)
	Walk(root, func(n Node) bool {
		sw, ok := n.(*BashPPSwitch)
		if !ok {
			return true
		}
		for i, arm := range sw.Arms {
			if len(arm.Stmts) == 0 {
				continue
			}
			branch, ok := arm.Stmts[len(arm.Stmts)-1].Cmd.(*BashPPBranch)
			if !ok || branch.Kw.Value != "fallthrough" {
				continue
			}
			if i+1 < len(sw.Arms) {
				allowedFallthrough[branch] = true
			} else {
				finalFallthrough[branch] = true
			}
		}
		return true
	})

	var controls []bashppControlKind
	var enteredControl []bool
	var problem *BashPPBranch
	var message string
	Walk(root, func(n Node) bool {
		if problem != nil {
			return false
		}
		if n == nil {
			last := len(enteredControl) - 1
			if enteredControl[last] {
				controls = controls[:len(controls)-1]
			}
			enteredControl = enteredControl[:last]
			return true
		}
		kind := bashppControlKind(0)
		switch n.(type) {
		case *BashPPFor:
			kind = bashppControlFor
		case *BashPPRange:
			kind = bashppControlRange
		case *BashPPSwitch:
			kind = bashppControlSwitch
		case *BashPPSelect:
			kind = bashppControlSelect
		}
		enteredControl = append(enteredControl, kind != 0)
		if kind != 0 {
			controls = append(controls, kind)
		}
		branch, ok := n.(*BashPPBranch)
		if !ok {
			return true
		}
		switch branch.Kw.Value {
		case "continue":
			valid := false
			for i := len(controls) - 1; i >= 0; i-- {
				if controls[i] == bashppControlFor || controls[i] == bashppControlRange {
					valid = true
					break
				}
			}
			if !valid {
				problem, message = branch, "bash++ continue is not inside a typed for or range loop"
			}
		case "fallthrough":
			hasSwitch := false
			for i := len(controls) - 1; i >= 0; i-- {
				if controls[i] == bashppControlSwitch {
					hasSwitch = true
					break
				}
			}
			switch {
			case !hasSwitch:
				problem, message = branch, "bash++ fallthrough is not inside an expression switch clause"
			case finalFallthrough[branch]:
				problem, message = branch, "bash++ fallthrough cannot appear in the final switch clause"
			case !allowedFallthrough[branch]:
				problem, message = branch, "bash++ fallthrough must be the final non-empty statement of a switch clause"
			}
		}
		return problem == nil
	})
	if problem != nil {
		p.posErr(problem.Pos(), "%s", message)
	}
}
