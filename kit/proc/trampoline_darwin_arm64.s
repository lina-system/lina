// SPDX-License-Identifier: Apache-2.0

#include "textflag.h"

// libproc is called through the ABI0 trampolines the linker binds to dyld.
TEXT ·libc_proc_listallpids_trampoline(SB),NOSPLIT,$0-0
	JMP	libc_proc_listallpids(SB)

TEXT ·libc_proc_pidinfo_trampoline(SB),NOSPLIT,$0-0
	JMP	libc_proc_pidinfo(SB)

TEXT ·libprocListAllPidsPC(SB),NOSPLIT,$0-8
	MOVD	$·libc_proc_listallpids_trampoline(SB), R0
	MOVD	R0, ret+0(FP)
	RET

TEXT ·libprocPidinfoPC(SB),NOSPLIT,$0-8
	MOVD	$·libc_proc_pidinfo_trampoline(SB), R0
	MOVD	R0, ret+0(FP)
	RET
