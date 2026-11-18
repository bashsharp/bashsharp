#include "textflag.h"

// func sigactionTrampolineAddr() uintptr
TEXT ·sigactionTrampolineAddr(SB),NOSPLIT,$0-8
	MOVD	$·sigactionTrampoline(SB), R0
	MOVD	R0, ret+0(FP)
	RET

TEXT ·sigactionTrampoline(SB),NOSPLIT,$0
	BL	libc_sigaction(SB)
	RET
