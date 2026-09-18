module github.com/qiangli/bashsharp

go 1.26.5

toolchain go1.27.1

require mvdan.cc/sh/v3 v3.13.1

require (
	golang.org/x/mod v0.29.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/term v0.41.0 // indirect
	golang.org/x/text v0.9.0 // indirect
	golang.org/x/tools v0.38.0 // indirect
)

// Bash# (formerly Bash++) is a language over the qiangli/sh fork of mvdan.cc/sh (the engine
// that carries the Bash++ evaluator, lowering compiler, Go front end and
// fences). Flat sibling, umbrella convention: dhnt/sh next to dhnt/bashsharp.
replace mvdan.cc/sh/v3 => ../sh
