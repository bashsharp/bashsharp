#include "textflag.h"

// func sigactionTrampolineAddr() uintptr
TEXT ·sigactionTrampolineAddr(SB),NOSPLIT,$0-8
	MOVQ	$·sigactionTrampoline(SB), AX
	MOVQ	AX, ret+0(FP)
	RET

TEXT ·sigactionTrampoline(SB),NOSPLIT,$0
	CALL	libc_sigaction(SB)
	RET
