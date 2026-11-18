package lower

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// RuntimeContext contains expressions, never global variable conventions. The
// private callable implementation must thread these through every nested call.
type RuntimeContext struct{ Context, Session, Channels string }

func (e *emitter) runtimeContext(c RuntimeContext) error {
	if c.Context == "" || c.Session == "" || c.Channels == "" {
		return e.fail(nil, CodeBridge, "channel lowering requires explicit context, session and ownership scope")
	}
	e.bridge = true
	return nil
}
func (e *emitter) runtimeMakeChannel(n *syntax.BashPPMakeChan, c RuntimeContext) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	if n.ChanType == nil {
		return "", e.fail(n, CodeType, "channel element type is missing")
	}
	var typ string
	var err error
	if n.ChanType.Element != nil {
		typ, err = e.typeExpr(n.ChanType.Element)
	} else if n.ChanType.Elem != nil {
		typ, err = e.typeSpelling(n.ChanType.Elem, n.ChanType.Elem.Value)
	} else {
		return "", e.fail(n, CodeType, "channel element type is missing")
	}
	if err != nil {
		return "", err
	}
	capacity := "0"
	if n.Capacity != nil {
		capacity, err = e.valueWord(n.Capacity)
		if err != nil {
			return "", err
		}
	}
	rt := e.prefix + "rt."
	return rt + "MustChannel(" + rt + "MakeChannel[" + typ + "](" + c.Channels + ", " + capacity + "))", nil
}
func (e *emitter) runtimeSend(n *syntax.BashPPSend, c RuntimeContext) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	channel, err := e.valueWord(n.Chan)
	if err != nil {
		return "", err
	}
	value, err := e.argument(n.Value)
	if err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	info, _ := e.projections.projectionLookup(n.Chan.Lit())
	if info.sourceType == "string" || info.capabilityElement != "" {
		elem := info.capabilityElement
		if elem == "" {
			elem = "string"
		}
		if elem == "string" && n.Value.Lit() != "" {
			value = strconv.Quote(n.Value.Lit())
		}
		guard := rt + "MustChannelOperation(" + rt + "RequireChannelBinding(" + e.program() + ".ResultSidecars,&" + channel + "," + strconv.Quote(n.Chan.Lit()) + "))\n"
		return guard + rt + "MustChannelOperation(" + rt + "SendCapability[" + elem + "](" + c.Context + "," + e.program() + ".ResultSidecars,&" + channel + "," + value + "))", nil
	}
	if strings.TrimSpace(info.sourceType) == "chan string" && n.Value.Lit() != "" {
		value = strconv.Quote(n.Value.Lit())
	}
	return rt + "MustChannelOperation(" + rt + "Send(" + c.Context + ", " + c.Session + ", " + c.Channels + ", " + channel + ", " + value + "))", nil
}
func (e *emitter) runtimeReceive(n *syntax.BashPPReceive, c RuntimeContext, commaOK bool) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	channel, err := e.valueWord(n.Chan)
	if err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	helper := "MustReceiveValue"
	if commaOK {
		helper = "MustReceive"
	}
	return rt + helper + "(" + rt + "Receive(" + c.Context + ", " + c.Session + ", " + c.Channels + ", " + channel + "))", nil
}
func (e *emitter) runtimeClose(n *syntax.BashPPClose, c RuntimeContext) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	channel, err := e.valueWord(n.Chan)
	if err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	return rt + "MustChannelOperation(" + rt + "CloseChannel(" + c.Channels + ", " + channel + "))", nil
}

// runtimeBody delegates nested statements to the compiler's ordinary dispatcher.
// In particular it must preserve selected-case binding scope and source maps.
type runtimeBody func([]*syntax.Stmt, RuntimeContext) (string, error)

func (e *emitter) runtimeSelect(n *syntax.BashPPSelect, c RuntimeContext, body runtimeBody) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	if body == nil {
		return "", e.fail(n, CodeBridge, "select needs a context-aware body dispatcher")
	}
	rt := e.prefix + "rt."
	var setup, arms strings.Builder
	var cases []string
	selected := e.prefix + "selection"
	for i, arm := range n.Cases {
		e.push()
		channel := e.prefix + "selectChannel" + strconv.Itoa(i)
		bind := ""
		entry := ""
		if arm.Default {
			entry = rt + "DefaultCase()"
		} else {
			var source *syntax.Word
			var receive *syntax.BashPPReceive
			var namesList []string
			switch comm := arm.Comm.(type) {
			case *syntax.BashPPSend:
				source = comm.Chan
				ch, err := e.valueWord(source)
				if err != nil {
					e.pop()
					return "", err
				}
				value, err := e.argument(comm.Value)
				if err != nil {
					e.pop()
					return "", err
				}
				fmt.Fprintf(&setup, "%s := %s\n", channel, ch)
				// The typed constructor preserves assignability of untyped constants.
				entry = rt + "SendCase(" + c.Channels + ", " + channel + ", " + value + ")"
			case *syntax.BashPPReceive:
				receive = comm
			case *syntax.BashPPShortDecl:
				receive = comm.Recv
				namesList = names(comm.Lhs)
			default:
				e.pop()
				return "", e.fail(arm, CodeUnsupported, "select communication requires send or receive declaration")
			}
			if receive != nil {
				ch, err := e.valueWord(receive.Chan)
				if err != nil {
					e.pop()
					return "", err
				}
				fmt.Fprintf(&setup, "%s := %s\n", channel, ch)
				entry = rt + "ReceiveCase(" + c.Channels + ", " + channel + ")"
				if len(namesList) > 0 {
					if len(namesList) > 2 {
						e.pop()
						return "", e.fail(arm, CodeType, "channel receive permits at most two values")
					}
					for _, name := range namesList {
						e.bind(name)
					}
					target := strings.Join(namesList, ", ")
					if len(namesList) == 1 {
						target += ", _"
					}
					bind = target + " := " + rt + "SelectedReceive(" + selected + ", " + channel + ")\n" + e.unused(namesList) + "\n"
				}
			}
			if entry == "" {
				e.pop()
				return "", e.fail(arm, CodeExpr, "missing select channel operation")
			}
		}
		// Materialize this case before evaluating the next case's operands.
		temp := e.prefix + "selectCase" + strconv.Itoa(i)
		fmt.Fprintf(&setup, "%s := %s\n", temp, entry)
		cases = append(cases, temp)
		text, err := body(arm.Stmts, c)
		e.pop()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&arms, "case %d:\n%s%s\n", i, bind, text)
	}
	return "{\n" + setup.String() + selected + " := " + rt + "MustSelect(" + rt + "SelectChannels(" + c.Context + ", " + c.Session + ", " + c.Channels + ", []" + e.prefix + "rt.ChannelCase{" + strings.Join(cases, ", ") + "}))\nswitch " + selected + ".Index {\n" + arms.String() + "}\n}", nil
}
func (e *emitter) runtimeChannelRange(n *syntax.BashPPRange, c RuntimeContext, body runtimeBody) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	if body == nil {
		return "", e.fail(n, CodeBridge, "channel range needs a context-aware body dispatcher")
	}
	if len(n.Names) > 1 {
		return "", e.fail(n, CodeType, "channel range permits at most one iteration variable")
	}
	channel, err := e.valueWord(n.Chan)
	if err != nil {
		return "", err
	}
	e.push()
	defer e.pop()
	name := "_"
	if len(n.Names) == 1 {
		name = n.Names[0].Value
		e.bind(name)
	}
	value, ok := e.prefix+"rangeValue", e.prefix+"rangeOK"
	statement := name + " := " + value
	if name == "_" {
		statement = "_ = " + value
	} else if !n.Define.IsValid() {
		statement = name + " = " + value
	}
	if name != "_" {
		statement += e.unused([]string{name})
	}
	text, err := body(n.Body.Stmts, c)
	if err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	temp := e.prefix + "rangeChannel"
	return "{\n" + temp + " := " + channel + "\nfor {\n" + value + ", " + ok + " := " + rt + "MustReceive(" + rt + "Receive(" + c.Context + ", " + c.Session + ", " + c.Channels + ", " + temp + "))\nif !" + ok + " { break }\n" + statement + "\n" + text + "\n}\n}", nil
}

// runtimeTaskCall must evaluate the callee/arguments into captureSetup at the
// launch site and emit invocation against the supplied child context. It must
// not call an unchanged public wrapper which would discard cancellation.
type runtimeTaskCall func(*syntax.BashPPCall, RuntimeContext) (captureSetup, invocation string, err error)

func (e *emitter) runtimeGo(n *syntax.BashPPGo, c RuntimeContext, call runtimeTaskCall) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	if call == nil {
		return "", e.fail(n, CodeBridge, "task launch requires private callable context threading")
	}
	child := RuntimeContext{Context: e.prefix + "taskContext", Session: e.prefix + "taskSession", Channels: c.Channels}
	setup, invocation, err := call(n.Call, child)
	if err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	// TaskContext aliases context.Context so generated imports remain explicit in
	// the single configured runtime dependency.
	return "{\n" + setup + "\n" + c.Session + ".Go(" + rt + "ChannelTask(func(" + child.Context + " " + rt + "TaskContext, " + child.Session + " *" + rt + "Session) error {\n" + invocation + "\nreturn nil\n}))\n}", nil
}
