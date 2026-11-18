package interp

import (
	"path/filepath"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 165, lane frames-1: the file a reported frame names.
//
// A frame's position is the directive-adjusted one Go's runtime reports.
// The front end already applies every `//line` directive to node positions
// — a Pos carries the adjusted line and column — but a Pos cannot carry a
// filename, so the source records where each directive changes it
// (syntax.SourceFile.LineDirectives) and the frame table consults that
// table at the frame's offset. The rules are the compiler's (its parser's
// updateBase): a cleared filename (`//line :N`) is reported as "??"; an
// absolute one is reported as written; a relative one is resolved against
// the directory of the source file it appears in. A position no directive
// governs reports the physical file, unchanged.

// goSourceClearedFilename is what the toolchain prints for a position whose
// line directive cleared the filename.
const goSourceClearedFilename = "??"

// goSourceFramePosition is the file and line the runtime reports for a
// frame executing at pos.
func (r *Runner) goSourceFramePosition(pos syntax.Pos) (string, uint) {
	return r.goSourceFrameFile(pos), pos.Line()
}

// goSourceFrameFile is the file the runtime reports for a frame executing
// at pos: the physical file of the source containing pos, unless a line
// directive of that source governs the offset.
func (r *Runner) goSourceFrameFile(pos syntax.Pos) string {
	if r.bashPPGoSourceFile == nil || !pos.IsValid() {
		return r.goSourceStackFile("")
	}
	source, ok := r.bashPPGoSourceFile.SourceAt(pos)
	if !ok {
		return r.goSourceStackFile("")
	}
	rel := pos.Offset() - source.Base
	name, governed := "", false
	for _, directive := range source.LineDirectives {
		if directive.Offset > rel {
			break
		}
		name, governed = directive.Filename, true
	}
	if !governed {
		return r.goSourceStackFile(source.Name)
	}
	given := source.Name
	if given == "" {
		given = r.filename
	}
	return goSourceDirectiveFile(name, given)
}

// goSourceDirectiveFile resolves a line directive's filename the way the
// compiler does: against the directory of the source file's name as it was
// given, when it names one. A name given without a directory leaves a
// relative directive as written; the go command names files that way.
func goSourceDirectiveFile(name, given string) string {
	if name == "" {
		return goSourceClearedFilename
	}
	name = filepath.Clean(name)
	if filepath.IsAbs(name) {
		return name
	}
	if dir := filepath.Dir(given); dir != "." {
		return filepath.Join(dir, name)
	}
	return name
}
