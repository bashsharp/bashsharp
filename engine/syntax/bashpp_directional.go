package syntax

// bashppChannelTypeWord is entered only inside a committed typed signature.
// The shell lexer owns < elsewhere; this function joins its arrow tokens only
// after that signature has established a type position.
func (p *Parser) bashppChannelTypeWord(first *Word) *Word {
	var words []*Word
	if first == nil {
		if p.tok != rdrIn {
			return nil
		}
		arrow := p.pos
		p.next()
		tail := p.getWord()
		head := bashppBareLit(tail)
		if head == nil || head.Value != "-chan" {
			return nil
		}
		first = p.wordOne(&Lit{ValuePos: arrow, ValueEnd: tail.End(), Value: "<-chan"})
	} else if head := bashppBareLit(first); head == nil || head.Value != "chan" {
		return nil
	}
	words = append(words, first)
	if p.tok == rdrIn {
		arrow := p.pos
		p.next()
		tail := p.getWord()
		head := bashppBareLit(tail)
		if head == nil || head.Value != "-" {
			return nil
		}
		words = append(words, p.wordOne(&Lit{ValuePos: arrow, ValueEnd: tail.End(), Value: "<-"}))
	}
	element := p.getWord()
	if head := bashppBareLit(element); head != nil && head.Value == "chan" {
		element = p.bashppChannelTypeWord(element)
	} else if element == nil && p.tok == rdrIn {
		element = p.bashppChannelTypeWord(nil)
	}
	if element == nil {
		return nil
	}
	return bashppJoinWords(append(words, element))
}
