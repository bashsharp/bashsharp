// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"os"
	"path/filepath"
	"strings"
)

// bashppEmbed claims `embed TYPE "PATH" [as ALIAS] [!RUNNER]` at column 1: a
// source fence whose body is the file's bytes, exactly as if written inline
// between `~~~TYPE [as ALIAS] [!RUNNER]` and a closing `~~~`. Any other shape
// stays an ordinary `embed` command (Class E). A relative PATH resolves
// against the directory of the file being parsed, the cwd when it has no name.
func (p *Parser) bashppEmbed(ce *CallExpr, redirs []*Redirect) *SourceBlock {
	if len(ce.Assigns) != 0 || len(redirs) != 0 || len(ce.Args) < 3 || ce.Pos().Col() != 1 {
		return nil
	}
	kw, language := bashppBareLit(ce.Args[0]), bashppBareLit(ce.Args[1])
	if kw == nil || kw.Value != "embed" || language == nil || !bashppValidSourceLanguage(language.Value) {
		return nil
	}
	q, ok := exactGoImportString(ce.Args[2])
	if !ok {
		return nil
	}
	lit := q.Parts[0].(*Lit)
	src := lit.Value
	if !strings.HasPrefix(src, "./") && !strings.HasPrefix(src, "../") || strings.ContainsAny(src, `\"`) {
		return nil
	}
	var fields []string
	for _, arg := range ce.Args[3:] {
		word := bashppBareLit(arg)
		if word == nil {
			return nil
		}
		fields = append(fields, word.Value)
	}
	alias, runner, ok := bashppSourceFenceTail(language.Value, fields)
	if !ok {
		return nil
	}
	block := &SourceBlock{
		FencePos: kw.ValuePos, Language: language,
		Src:     &Lit{ValuePos: lit.ValuePos, ValueEnd: lit.ValueEnd, Value: src},
		BodyPos: ce.End(),
	}
	// The tail was validated above: `as ALIAS`, `!RUNNER`, or `as !RUNNER`.
	for _, arg := range ce.Args[3:] {
		word := bashppBareLit(arg)
		switch {
		case word.Value == "as":
		case strings.HasPrefix(word.Value, "!"):
			block.Runner = &Lit{ValuePos: posAddCol(word.ValuePos, 1), ValueEnd: word.ValueEnd, Value: runner}
			if alias == runner && block.Alias == nil {
				block.Alias = block.Runner
			}
		default:
			block.Alias = word
		}
	}
	path := src
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(p.f.Name), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		p.posErr(lit.ValuePos, "embed: %v", err)
		return block
	}
	block.Body = string(data)
	if alias == "" {
		p.bashppRegisterSourceBlockFuncs(language.Value, block.Body)
	}
	return block
}
