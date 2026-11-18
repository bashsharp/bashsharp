package gosource

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"reflect"
	"strings"
	"unicode/utf8"
)

// gcSourceImage returns the bytes gc's scanner tokenises. gc's source layer
// (cmd/compile/internal/syntax/source.go nextch) reports and then drops
// every NUL byte, every byte that is not part of a valid UTF-8 encoding and
// every byte order mark after the first character, while its positions keep
// counting the dropped bytes. go/scanner reports the same bytes but keeps
// them — an ILLEGAL token in code, the bytes themselves inside a literal —
// so after such a byte go/parser's tree differs from gc's (an identifier
// split in two, a declaration lost to recovery) and go/types reports on
// the difference. dropped lists the original offsets removed from the
// image, in order; it is empty when the image is src itself. A leading
// byte order mark is not dropped: both scanners skip it themselves.
func gcSourceImage(src []byte) (image []byte, dropped []int) {
	for i := 0; i < len(src); {
		b := src[i]
		if b < utf8.RuneSelf {
			if b == 0 {
				dropped = append(dropped, i)
			}
			i++
			continue
		}
		r, w := utf8.DecodeRune(src[i:])
		switch {
		case r == utf8.RuneError && w == 1:
			dropped = append(dropped, i)
		case r == '\uFEFF' && i > 0:
			for j := 0; j < w; j++ {
				dropped = append(dropped, i+j)
			}
		}
		i += w
	}
	if len(dropped) == 0 {
		return src, nil
	}
	image = make([]byte, 0, len(src)-len(dropped))
	next := 0
	for i, b := range src {
		if next < len(dropped) && dropped[next] == i {
			next++
			continue
		}
		image = append(image, b)
	}
	return image, dropped
}

// gcScannerImage returns image with every character that go/scanner
// rejects as an ILLEGAL token rewritten into what gc's scanner makes of
// it, and whether there was one (the result is image itself when there is
// none). gc's scanner (scanner.go next,
// atIdentChar) has no illegal token: a character below utf8.RuneSelf that
// starts no token is reported ("invalid character U+0024 '$'") and
// skipped, so it separates the tokens around it like a space; every other
// character is an identifier character — at a token's start it begins an
// identifier, after one it continues it — reported ("invalid character
// U+2639 '☹' in identifier") and kept in the identifier's text. go/scanner
// returns the character as an ILLEGAL token instead, after which go/parser
// recovers by dropping the declaration or statement around it and go/types
// reports on what was lost ("undefined: x" for `var ☹x int`, where types2
// declares ☹x). The rewrite is a space for the former and a letter of the
// same encoded width for the latter, so go/scanner tokenises the image as
// gc's scanner tokenises the source, with the same token boundaries and
// byte offsets; parseGoFile then restores each token's original text, so
// the identifier's name is ☹x for go/types as it is for types2, which
// neither checker looks up when it is not a valid name (typexpr.go ident,
// call.go selector: isValidName). Characters inside a literal or a comment
// are never rejected by either scanner and are left alone.
func gcScannerImage(image []byte) (out []byte, rewritten bool) {
	scratch := token.NewFileSet()
	file := scratch.AddFile("", -1, len(image))
	var s scanner.Scanner
	s.Init(file, image, nil, 0)
	out = image
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.ILLEGAL || lit == "" {
			continue
		}
		offset := file.Offset(pos)
		r, w := utf8.DecodeRune(image[offset:])
		if string(r) != lit {
			continue
		}
		if !rewritten {
			out, rewritten = append([]byte(nil), image...), true
		}
		switch {
		case r < utf8.RuneSelf:
			out[offset] = ' '
		default:
			// A letter of each encoded width: U+00E9, U+4E16, U+1D400.
			letter := [...]rune{2: 'é', 3: '世', 4: '𝐀'}[w]
			utf8.EncodeRune(out[offset:offset+w], letter)
		}
	}
	return out, rewritten
}

// parseGoFile is parser.ParseFile on the bytes gc's scanner sees, with the
// tree positioned in the original file. When gcSourceImage drops nothing
// and gcScannerImage rewrites nothing it is parser.ParseFile itself.
// Otherwise the image is parsed in a scratch file set, the original
// registered in fset with its own line table and the same line directives,
// and every position in the tree mapped from the image back to the
// original byte, so go/types checks the tree types2 checks and reports at
// the columns gc reports. Token boundaries are the image's; a token's text
// is gc's segment of the original — the raw bytes from its first to its
// last character, dropped and rewritten bytes included (scanner.go ident,
// stdString: s.segment()) — so an identifier or literal written across a
// dropped byte keeps that byte in its name or value, and an identifier
// written with an invalid character keeps the character in its name, as
// they do for types2. The syntax verdict has already rejected such a
// source (each dropped byte and each invalid character is one of gc's
// rows), so the tree is checked, never converted or run.
func parseGoFile(fset *token.FileSet, name string, src []byte, mode parser.Mode) (*ast.File, error) {
	image, dropped := gcSourceImage(src)
	image, rewritten := gcScannerImage(image)
	if len(dropped) == 0 && !rewritten {
		return parser.ParseFile(fset, name, src, mode)
	}
	scratch := token.NewFileSet()
	f, err := parser.ParseFile(scratch, name, image, mode)
	if f == nil {
		return nil, err
	}
	sf := scratch.File(f.FileStart)
	tf := fset.AddFile(name, -1, len(src))
	tf.SetLinesForContent(src)
	// The original offset of an image byte: the image offset plus the
	// dropped bytes that precede it in the original.
	original := func(offset int) int {
		k := 0
		for k < len(dropped) && dropped[k] <= offset+k {
			k++
		}
		return offset + k
	}
	// go/scanner recorded each line directive on the scratch file at the
	// offset where it takes effect; the same record is made on the
	// original at the mapped offset.
	for _, group := range f.Comments {
		for _, cm := range group.List {
			var effect int
			switch {
			case strings.HasPrefix(cm.Text, "//line "), strings.HasPrefix(cm.Text, "//line\t"):
				line := sf.PositionFor(cm.End(), false).Line
				if line >= sf.LineCount() {
					continue
				}
				effect = sf.Offset(sf.LineStart(line + 1))
			case strings.HasPrefix(cm.Text, "/*line "), strings.HasPrefix(cm.Text, "/*line\t"):
				effect = sf.Offset(cm.End())
			default:
				continue
			}
			adjusted := sf.PositionFor(sf.Pos(effect), true)
			plain := sf.PositionFor(sf.Pos(effect), false)
			if adjusted == plain {
				continue
			}
			tf.AddLineColumnInfo(original(effect), adjusted.Filename, adjusted.Line, adjusted.Column)
		}
	}
	// gc's segment runs from the token's first character to the character
	// that ends the token, so dropped bytes between its last character and
	// the next one belong to it as well (an identifier's, a number's); a
	// literal ends at its own quote.
	segment := func(pos token.Pos, text string) string {
		if !pos.IsValid() || text == "" {
			return text
		}
		start := sf.Offset(pos)
		end := original(start+len(text)-1) + 1
		for k := 0; k < len(dropped); k++ {
			if dropped[k] == end {
				end++
			}
		}
		return string(src[original(start):end])
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			n.Name = segment(n.NamePos, n.Name)
		case *ast.BasicLit:
			n.Value = segment(n.ValuePos, n.Value)
		}
		return true
	})
	remap := func(p token.Pos) token.Pos {
		if !p.IsValid() {
			return p
		}
		return tf.Pos(original(sf.Offset(p)))
	}
	remapPositions(reflect.ValueOf(f), remap, map[uintptr]bool{})
	f.FileStart, f.FileEnd = tf.Pos(0), tf.Pos(len(src))
	return f, err
}

var tokenPosType = reflect.TypeFor[token.Pos]()

// remapPositions rewrites every token.Pos reachable from v through structs,
// pointers, slices, maps and interfaces (ast.Object.Decl points back into
// the tree; visited stops the cycle).
func remapPositions(v reflect.Value, remap func(token.Pos) token.Pos, visited map[uintptr]bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		if visited[v.Pointer()] {
			return
		}
		visited[v.Pointer()] = true
		remapPositions(v.Elem(), remap, visited)
	case reflect.Interface:
		if !v.IsNil() {
			remapPositions(v.Elem(), remap, visited)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if f.Type() == tokenPosType {
				if f.CanSet() {
					f.Set(reflect.ValueOf(remap(token.Pos(f.Int()))))
				}
				continue
			}
			remapPositions(f, remap, visited)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			remapPositions(v.Index(i), remap, visited)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			remapPositions(it.Value(), remap, visited)
		}
	}
}
