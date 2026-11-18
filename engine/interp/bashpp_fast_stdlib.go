package interp

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/bits"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The table is deliberately made of linked, typed symbols. A Go import alias
// is authenticated through req.Imports before a name is looked up here.
var fastStrconvSymbols = map[string]reflect.Value{
	"Itoa":               reflect.ValueOf(strconv.Itoa),
	"FormatBool":         reflect.ValueOf(strconv.FormatBool),
	"FormatInt":          reflect.ValueOf(strconv.FormatInt),
	"FormatUint":         reflect.ValueOf(strconv.FormatUint),
	"FormatFloat":        reflect.ValueOf(strconv.FormatFloat),
	"FormatComplex":      reflect.ValueOf(strconv.FormatComplex),
	"Quote":              reflect.ValueOf(strconv.Quote),
	"QuoteToASCII":       reflect.ValueOf(strconv.QuoteToASCII),
	"QuoteToGraphic":     reflect.ValueOf(strconv.QuoteToGraphic),
	"QuoteRune":          reflect.ValueOf(strconv.QuoteRune),
	"QuoteRuneToASCII":   reflect.ValueOf(strconv.QuoteRuneToASCII),
	"QuoteRuneToGraphic": reflect.ValueOf(strconv.QuoteRuneToGraphic),
	"CanBackquote":       reflect.ValueOf(strconv.CanBackquote),
	"IsPrint":            reflect.ValueOf(strconv.IsPrint),
	"IsGraphic":          reflect.ValueOf(strconv.IsGraphic),
	"AppendBool":         reflect.ValueOf(strconv.AppendBool),
	"AppendInt":          reflect.ValueOf(strconv.AppendInt),
	"AppendUint":         reflect.ValueOf(strconv.AppendUint),
	"AppendFloat":        reflect.ValueOf(strconv.AppendFloat),
	"AppendQuote":        reflect.ValueOf(strconv.AppendQuote),
}

var fastPureSymbols = map[string]map[string]reflect.Value{
	"math": {
		"Abs": reflect.ValueOf(math.Abs), "Ceil": reflect.ValueOf(math.Ceil), "Cos": reflect.ValueOf(math.Cos),
		"Exp": reflect.ValueOf(math.Exp), "Floor": reflect.ValueOf(math.Floor), "IsInf": reflect.ValueOf(math.IsInf),
		"IsNaN": reflect.ValueOf(math.IsNaN), "Log": reflect.ValueOf(math.Log), "Max": reflect.ValueOf(math.Max),
		"Min": reflect.ValueOf(math.Min), "Mod": reflect.ValueOf(math.Mod), "Pow": reflect.ValueOf(math.Pow),
		"Round": reflect.ValueOf(math.Round), "Signbit": reflect.ValueOf(math.Signbit), "Sin": reflect.ValueOf(math.Sin),
		"Sqrt": reflect.ValueOf(math.Sqrt), "Tan": reflect.ValueOf(math.Tan), "Trunc": reflect.ValueOf(math.Trunc),
	},
	"math/bits": {
		"LeadingZeros": reflect.ValueOf(bits.LeadingZeros), "LeadingZeros32": reflect.ValueOf(bits.LeadingZeros32),
		"LeadingZeros64": reflect.ValueOf(bits.LeadingZeros64), "Len": reflect.ValueOf(bits.Len),
		"Len32": reflect.ValueOf(bits.Len32), "Len64": reflect.ValueOf(bits.Len64),
		"OnesCount": reflect.ValueOf(bits.OnesCount), "OnesCount32": reflect.ValueOf(bits.OnesCount32),
		"OnesCount64": reflect.ValueOf(bits.OnesCount64), "Reverse": reflect.ValueOf(bits.Reverse),
		"ReverseBytes": reflect.ValueOf(bits.ReverseBytes), "TrailingZeros": reflect.ValueOf(bits.TrailingZeros),
		"TrailingZeros32": reflect.ValueOf(bits.TrailingZeros32), "TrailingZeros64": reflect.ValueOf(bits.TrailingZeros64),
	},
	"strings": {
		"Contains": reflect.ValueOf(strings.Contains), "Count": reflect.ValueOf(strings.Count),
		"EqualFold": reflect.ValueOf(strings.EqualFold), "HasPrefix": reflect.ValueOf(strings.HasPrefix),
		"HasSuffix": reflect.ValueOf(strings.HasSuffix), "Index": reflect.ValueOf(strings.Index),
		"LastIndex": reflect.ValueOf(strings.LastIndex), "Repeat": reflect.ValueOf(strings.Repeat),
		"Replace": reflect.ValueOf(strings.Replace), "ReplaceAll": reflect.ValueOf(strings.ReplaceAll),
		"ToLower": reflect.ValueOf(strings.ToLower), "ToUpper": reflect.ValueOf(strings.ToUpper),
		"Trim": reflect.ValueOf(strings.Trim), "TrimPrefix": reflect.ValueOf(strings.TrimPrefix),
		"TrimSpace": reflect.ValueOf(strings.TrimSpace), "TrimSuffix": reflect.ValueOf(strings.TrimSuffix),
	},
	"bytes": {
		"Contains": reflect.ValueOf(bytes.Contains), "Count": reflect.ValueOf(bytes.Count),
		"Equal": reflect.ValueOf(bytes.Equal), "HasPrefix": reflect.ValueOf(bytes.HasPrefix),
		"HasSuffix": reflect.ValueOf(bytes.HasSuffix), "Index": reflect.ValueOf(bytes.Index),
		"LastIndex": reflect.ValueOf(bytes.LastIndex), "Repeat": reflect.ValueOf(bytes.Repeat),
		"Replace": reflect.ValueOf(bytes.Replace), "ReplaceAll": reflect.ValueOf(bytes.ReplaceAll),
		"ToLower": reflect.ValueOf(bytes.ToLower), "ToUpper": reflect.ValueOf(bytes.ToUpper),
		"TrimSpace": reflect.ValueOf(bytes.TrimSpace),
	},
	"unicode": {
		"IsDigit": reflect.ValueOf(unicode.IsDigit), "IsLetter": reflect.ValueOf(unicode.IsLetter),
		"IsSpace": reflect.ValueOf(unicode.IsSpace), "ToLower": reflect.ValueOf(unicode.ToLower),
		"ToUpper": reflect.ValueOf(unicode.ToUpper),
	},
	"unicode/utf8": {
		"DecodeRune": reflect.ValueOf(utf8.DecodeRune), "DecodeRuneInString": reflect.ValueOf(utf8.DecodeRuneInString),
		"RuneCount": reflect.ValueOf(utf8.RuneCount), "RuneCountInString": reflect.ValueOf(utf8.RuneCountInString),
		"RuneLen": reflect.ValueOf(utf8.RuneLen), "Valid": reflect.ValueOf(utf8.Valid),
		"ValidString": reflect.ValueOf(utf8.ValidString),
	},
	"fmt": {
		"Sprint": reflect.ValueOf(fmt.Sprint), "Sprintln": reflect.ValueOf(fmt.Sprintln),
		"Sprintf": reflect.ValueOf(fmt.Sprintf),
	},
}

// Each entry is a method of one session-owned generator, except Seed, which
// is handled below. Keep this set in sync with the source-level ownership
// proof; an uncovered package-level operation disables the whole package.
var fastRandMethods = map[string]string{
	"Int": "Int", "Int31": "Int31", "Int31n": "Int31n",
	"Int63": "Int63", "Int63n": "Int63n", "Intn": "Intn",
	"Uint": "Uint", "Uint32": "Uint32", "Uint64": "Uint64",
	"Float32": "Float32", "Float64": "Float64",
	"NormFloat64": "NormFloat64", "ExpFloat64": "ExpFloat64",
}

// goSourceFastStdlibCall returns handled=false for every shape outside the
// small value-only contract. Fallback then follows the ordinary worker path.
func goSourceFastStdlibCall(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) (values []bashPPBridgeValue, handled bool, err error) {
	if req.disableFastStdlib || q.Op != "call" || q.Receiver != nil || q.Instance != "" || q.Spread || len(q.Transfers) != 0 || len(q.SliceBuffers) != 0 || q.FormatOnly != "" {
		return nil, false, nil
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || alias == "" || name == "" {
		return nil, false, nil
	}
	path := req.Imports[alias]
	var fn reflect.Value
	switch path {
	case "strconv":
		fn = fastStrconvSymbols[name]
	case "math/rand":
		if req.Bridge == nil || !req.Bridge.fastRandAllowed(req, alias) {
			return nil, false, nil
		}
		if name != "Seed" && fastRandMethods[name] == "" {
			return nil, false, nil
		}
	default:
		fn = fastPureSymbols[path][name]
	}
	if path != "math/rand" && !fn.IsValid() {
		return nil, false, nil
	}
	for _, arg := range q.Args {
		if !fastPlainBridgeValue(arg) {
			return nil, false, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if err := validateLocalTransport(req, q); err != nil {
		return nil, true, err
	}
	if path == "math/rand" {
		req.Bridge.fastRandMu.Lock()
		defer req.Bridge.fastRandMu.Unlock()
		if req.Bridge.fastRand == nil {
			var seed [8]byte
			if fastRandDebugSetting(req, "randautoseed") == "0" {
				binary.LittleEndian.PutUint64(seed[:], 1)
			} else {
				if _, err := crand.Read(seed[:]); err != nil {
					return nil, true, err
				}
			}
			req.Bridge.fastRand = rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(seed[:]))))
		}
		if name == "Seed" {
			fn = reflect.ValueOf(req.Bridge.fastRand.Seed)
		} else {
			fn = reflect.ValueOf(req.Bridge.fastRand).MethodByName(fastRandMethods[name])
		}
	}
	if !fn.IsValid() {
		return nil, false, nil
	}
	sig := fn.Type()
	if !sig.IsVariadic() && sig.NumIn() != len(q.Args) || sig.IsVariadic() && len(q.Args) < sig.NumIn()-1 {
		return nil, false, nil
	}
	args := make([]reflect.Value, len(q.Args))
	for i, arg := range q.Args {
		var target reflect.Type
		if sig.IsVariadic() && i >= sig.NumIn()-1 {
			if path != "fmt" {
				return nil, false, nil
			}
			var ok bool
			target, ok = fastDynamicScalarType(arg)
			if !ok {
				return nil, false, nil
			}
		} else {
			target = sig.In(i)
		}
		v, ok := fastDecodeScalar(arg, target)
		if !ok {
			return nil, false, nil
		}
		args[i] = v
	}
	for i := 0; i < sig.NumOut(); i++ {
		if !fastPlainType(sig.Out(i)) {
			return nil, false, nil
		}
	}
	if path == "math/rand" && name == "Seed" && !fastRandSeedActive(req) {
		// Since Go 1.24 the package-level Seed is a no-op by default.
		return nil, true, nil
	}
	defer func() {
		if recover() != nil {
			// Preserve the worker's panic translation and interpreter unwind.
			// The admitted functions validate before changing observable state.
			values, handled, err = nil, false, nil
		}
	}()
	handled = true
	results := fn.Call(args)
	values = make([]bashPPBridgeValue, len(results))
	for i, result := range results {
		values[i] = fastEncodeScalar(result)
	}
	return values, handled, nil
}

func fastPlainBridgeValue(v bashPPBridgeValue) bool {
	if v.Origin != 0 || v.Storage != 0 || v.Handle != 0 || v.Session != "" || v.Interface != "" ||
		v.Callbacks || v.Function || v.NativeTypeID != 0 || v.Signature != "" || v.LocalWriter != "" ||
		len(v.CallArgs) != 0 || v.ReaderLength != 0 || len(v.ReaderBuffer) != 0 || v.Offset != 0 ||
		v.sliceView != nil || v.localReflect != nil || v.localCell != nil || v.reflectCopy ||
		v.reflectFunction || v.reflectFunc != nil || v.deferredNativeComposite || v.copiedResults ||
		v.newCallback || v.callRefusal != "" || v.localRefusal != "" ||
		v.Kind != "slice" && len(v.Elements) != 0 || len(v.Fields) != 0 || len(v.Entries) != 0 || v.Within != nil {
		return false
	}
	switch v.Kind {
	case "bool", "int", "uint", "float", "complex", "string":
		return true
	case "slice", "nil":
		if v.Type != "[]uint8" && v.Type != "[]byte" || v.Capacity != len(v.Elements) || v.Length > v.Capacity {
			return false
		}
		for _, elem := range v.Elements {
			if !fastPlainBridgeValue(elem) || elem.Kind != "uint" || elem.Type != "uint8" {
				return false
			}
		}
		return true
	}
	return false
}

func fastPlainType(t reflect.Type) bool {
	if t == reflect.TypeOf([]byte(nil)) {
		return true
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return true
	}
	return false
}

func fastDecodeScalar(v bashPPBridgeValue, target reflect.Type) (reflect.Value, bool) {
	if !fastPlainType(target) || v.Type != "" && v.Type != target.String() &&
		!(target == reflect.TypeOf([]byte(nil)) && v.Type == "[]byte") &&
		!(target.Kind() == reflect.Uint8 && v.Type == "byte") &&
		!(target.Kind() == reflect.Int32 && v.Type == "rune") {
		return reflect.Value{}, false
	}
	if target == reflect.TypeOf([]byte(nil)) {
		if v.Kind == "nil" {
			return reflect.Zero(target), true
		}
		if v.Kind != "slice" {
			return reflect.Value{}, false
		}
		b := make([]byte, v.Length, v.Capacity)
		full := b[:cap(b)]
		for i, e := range v.Elements {
			n, err := strconv.ParseUint(e.Text, 10, 8)
			if err != nil || e.Kind != "uint" || e.Type != "uint8" {
				return reflect.Value{}, false
			}
			full[i] = byte(n)
		}
		return reflect.ValueOf(b), true
	}
	r := reflect.New(target).Elem()
	var err error
	switch target.Kind() {
	case reflect.Bool:
		var x bool
		x, err = strconv.ParseBool(v.Text)
		if v.Kind != "bool" {
			return reflect.Value{}, false
		}
		r.SetBool(x)
	case reflect.String:
		if v.Kind != "string" {
			return reflect.Value{}, false
		}
		r.SetString(v.stringText())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var x int64
		x, err = strconv.ParseInt(v.Text, 10, 64)
		if v.Kind != "int" || err == nil && r.OverflowInt(x) {
			return reflect.Value{}, false
		}
		r.SetInt(x)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		var x uint64
		x, err = strconv.ParseUint(v.Text, 10, 64)
		if v.Kind != "uint" || err == nil && r.OverflowUint(x) {
			return reflect.Value{}, false
		}
		r.SetUint(x)
	case reflect.Float32, reflect.Float64:
		if bits, ok := bashPPNaNBitsScalar(v.Text, v.Type); ok {
			if target.Kind() == reflect.Float32 {
				if bits.nanWidth == 32 {
					r.Set(reflect.ValueOf(math.Float32frombits(uint32(bits.nanBits))).Convert(target))
				} else {
					r.SetFloat(float64(float32(math.Float64frombits(bits.nanBits))))
				}
			} else {
				if bits.nanWidth == 64 {
					r.Set(reflect.ValueOf(math.Float64frombits(bits.nanBits)).Convert(target))
				} else {
					r.SetFloat(float64(math.Float32frombits(uint32(bits.nanBits))))
				}
			}
			return r, true
		}
		var x float64
		x, err = strconv.ParseFloat(v.Text, target.Bits())
		if v.Kind != "float" {
			return reflect.Value{}, false
		}
		r.SetFloat(x)
	case reflect.Complex64, reflect.Complex128:
		var x complex128
		x, err = strconv.ParseComplex(v.Text, target.Bits())
		if v.Kind != "complex" {
			return reflect.Value{}, false
		}
		r.SetComplex(x)
	}
	return r, err == nil
}

func fastEncodeScalar(v reflect.Value) bashPPBridgeValue {
	if v.Type() == reflect.TypeOf([]byte(nil)) {
		if v.IsNil() {
			return bashPPBridgeValue{Kind: "nil", Type: "[]uint8", NativeType: "[]uint8"}
		}
		out := bashPPBridgeValue{Kind: "slice", Type: "[]uint8", NativeType: "[]uint8", Length: v.Len(), Capacity: v.Cap(), Elements: make([]bashPPBridgeValue, v.Cap())}
		full := v.Slice3(0, v.Cap(), v.Cap())
		for i := 0; i < v.Cap(); i++ {
			out.Elements[i] = fastEncodeScalar(full.Index(i))
		}
		return out
	}
	out := bashPPBridgeValue{Type: v.Type().String(), NativeType: v.Type().String()}
	switch v.Kind() {
	case reflect.String:
		out.Kind, out.Text = "string", v.String()
		out.Bytes = []byte(out.Text)
	case reflect.Bool:
		out.Kind, out.Text = "bool", strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out.Kind, out.Text = "int", strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		out.Kind, out.Text = "uint", strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		out.Kind, out.Text = "float", strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits())
		if math.IsNaN(v.Float()) {
			if v.Kind() == reflect.Float32 {
				out.Text = bashPPNaNBitsText(uint64(math.Float32bits(v.Convert(reflect.TypeFor[float32]()).Interface().(float32))), 32)
			} else {
				out.Text = bashPPNaNBitsText(math.Float64bits(v.Convert(reflect.TypeFor[float64]()).Interface().(float64)), 64)
			}
		}
	case reflect.Complex64, reflect.Complex128:
		out.Kind, out.Text = "complex", strconv.FormatComplex(v.Complex(), 'g', -1, v.Type().Bits())
	}
	return out
}

var fastFormatTypes = map[string]reflect.Type{
	"bool": reflect.TypeOf(false), "string": reflect.TypeOf(""),
	"int": reflect.TypeOf(int(0)), "int8": reflect.TypeOf(int8(0)),
	"int16": reflect.TypeOf(int16(0)), "int32": reflect.TypeOf(int32(0)),
	"int64": reflect.TypeOf(int64(0)), "rune": reflect.TypeOf(rune(0)),
	"uint": reflect.TypeOf(uint(0)), "uint8": reflect.TypeOf(uint8(0)),
	"uint16": reflect.TypeOf(uint16(0)), "uint32": reflect.TypeOf(uint32(0)),
	"uint64": reflect.TypeOf(uint64(0)), "uintptr": reflect.TypeOf(uintptr(0)),
	"byte": reflect.TypeOf(byte(0)), "float32": reflect.TypeOf(float32(0)),
	"float64":   reflect.TypeOf(float64(0)),
	"complex64": reflect.TypeOf(complex64(0)), "complex128": reflect.TypeOf(complex128(0)),
}

func fastDynamicScalarType(v bashPPBridgeValue) (reflect.Type, bool) {
	t := fastFormatTypes[v.Type]
	if t == nil && v.Type == "" {
		switch v.Kind {
		case "bool":
			t = fastFormatTypes["bool"]
		case "int":
			t = fastFormatTypes["int"]
		case "uint":
			t = fastFormatTypes["uint"]
		case "float":
			t = fastFormatTypes["float64"]
		case "complex":
			t = fastFormatTypes["complex128"]
		case "string":
			t = fastFormatTypes["string"]
		}
	}
	return t, t != nil && fastPlainType(t)
}

// Only direct calls to covered top-level functions may share the session's
// interpreter-owned random state. An unreadable or composite source refuses
// the optimization. A selector used as a function value also refuses it.
func fastRandSourceProof(req bashPPEvalRequest, alias string) bool {
	if len(req.RootFiles) > 1 {
		return false
	}
	if len(req.CompanionFiles) != 0 || len(req.NativeFuncs) != 0 || len(req.MappedCompanions) != 0 || len(req.CgoPackages) != 0 {
		return false
	}
	// Other linked dependency code could call math/rand's global functions
	// while this program's calls use the local generator. Admit only packages
	// whose entry points do not do that.
	for _, path := range req.Imports {
		switch path {
		case "math/rand", "strconv", "fmt", "bytes", "strings":
		default:
			return false
		}
	}
	if req.SourceFile == "" {
		return false
	}
	data, err := os.ReadFile(req.SourceFile)
	if err != nil {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), req.SourceFile, data, 0)
	if err != nil {
		return false
	}
	approved := make(map[*ast.SelectorExpr]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == alias && (sel.Sel.Name == "Seed" || fastRandMethods[sel.Sel.Name] != "") {
					approved[sel] = true
				}
			}
		}
		return true
	})
	valid := true
	ast.Inspect(file, func(n ast.Node) bool {
		// A local declaration with the import's spelling can redirect one of
		// the selectors to a different value. Refuse the package as a unit.
		switch decl := n.(type) {
		case *ast.AssignStmt:
			if decl.Tok == token.DEFINE {
				for _, lhs := range decl.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == alias {
						valid = false
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range decl.Names {
				if id.Name == alias {
					valid = false
				}
			}
		case *ast.Field:
			for _, id := range decl.Names {
				if id.Name == alias {
					valid = false
				}
			}
		case *ast.RangeStmt:
			if decl.Tok == token.DEFINE {
				for _, item := range []ast.Expr{decl.Key, decl.Value} {
					if id, ok := item.(*ast.Ident); ok && id.Name == alias {
						valid = false
					}
				}
			}
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == alias && !approved[sel] {
				valid = false
			}
		}
		return valid
	})
	return valid
}

func (s *bashPPNativeSession) fastRandAllowed(req bashPPEvalRequest, alias string) bool {
	s.fastRandMu.Lock()
	defer s.fastRandMu.Unlock()
	if s.fastRandProofSeen == nil {
		s.fastRandProofSeen = make(map[string]bool)
		s.fastRandProof = make(map[string]bool)
	}
	key := req.SourceFile + "\x00" + alias
	if !s.fastRandProofSeen[key] {
		s.fastRandProof[key] = fastRandSourceProof(req, alias)
		s.fastRandProofSeen[key] = true
	}
	return s.fastRandProof[key]
}

func fastRandSeedActive(req bashPPEvalRequest) bool {
	return fastRandDebugSetting(req, "randseednop") == "0"
}

func fastRandDebugSetting(req bashPPEvalRequest, name string) string {
	debug := os.Getenv("GODEBUG")
	for _, pair := range req.Env {
		if value, ok := strings.CutPrefix(pair, "GODEBUG="); ok {
			debug = value
		}
	}
	// The original program's default GODEBUG (its module's go directive,
	// godebug lines and //go:debug directives) applies beneath the
	// environment, exactly as in a native build and as the worker link
	// replays it; see bashPPDefaultGODEBUG.
	debug = fastProgramDefaultGODEBUG(req) + "," + debug
	value := ""
	for _, setting := range strings.Split(debug, ",") {
		if selected, ok := strings.CutPrefix(setting, name+"="); ok {
			value = selected
		}
	}
	return value
}

// fastDefaultGODEBUG caches the default GODEBUG of each original program
// directory: establishing it runs the go command once.
var fastDefaultGODEBUG sync.Map

func fastProgramDefaultGODEBUG(req bashPPEvalRequest) string {
	key := req.SourceDir
	if key == "" {
		key = bashPPModuleRequest(req).Dir
	}
	if cached, ok := fastDefaultGODEBUG.Load(key); ok {
		return cached.(string)
	}
	value := bashPPDefaultGODEBUG(context.Background(), req)
	fastDefaultGODEBUG.Store(key, value)
	return value
}
