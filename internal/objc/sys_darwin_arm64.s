#include "textflag.h"
#include "go_asm.h"
#include "funcdata.h"
#include "abi_arm64.h"

// _crosscall2 expects a call to the ABIInternal function
// However, the tag <ABIInternal> is only available in the runtime :(
// This is a small wrapper function that moves the parameter from R0 to the stack
// where the Go function can find it. It then branches without link.
TEXT callbackWrapInternal(SB), NOSPLIT, $0-0
    MOVD R0, 8(RSP)
    B ·callbackWrap(SB)
    RET

TEXT ·callbackasm1(SB), NOSPLIT, $208-0
    NO_LOCAL_POINTERS

    // On entry, the trampoline in zcallback_windows_arm64.s left
    // the callback index in R12 (which is volatile in the C ABI).

    // Save callback register arguments R0-R7.
    // We do this at the top of the frame so they're contiguous with stack arguments.
    // The 7*8 setting up R14 looks like a bug but is not: the eighth word
    // is the space the assembler reserved for our caller's frame pointer,
    // but we are not called from Go so that space is ours to use,
    // and we must to be contiguous with the stack arguments.
    MOVD	$arg0-(7*8)(SP), R14
    STP	(R0, R1), (0*8)(R14)
    STP	(R2, R3), (2*8)(R14)
    STP	(R4, R5), (4*8)(R14)
    STP	(R6, R7), (6*8)(R14)

    // Push C callee-save registers R19-R28.
    // LR, FP already saved.
    //SAVE_R19_TO_R28(8*9)

    // Create a struct callbackArgs on our stack.
    MOVD	$cbargs-(18*8+callbackArgs__size)(SP), R13
    MOVD	R12, callbackArgs_index(R13)	// callback index
    MOVD	R14, R0
    MOVD	R0, callbackArgs_args(R13)		// address of args vector
    MOVD	$0, R0
    MOVD	R0, callbackArgs_result(R13)	// result

    MOVD $callbackWrapInternal(SB), R0 //fn unsafe.Pointer
    MOVD R13, R1 // frame (&callbackArgs{...})
    MOVD $0, R2 // n uintptr
    MOVD $0, R3 // ctxt uintptr
    STP	(R0, R1), (1*8)(RSP)
    MOVD	R2, (3*8)(RSP)
    // Initialize Go ABI environment
    BL	_crosscall2(SB)

    // Get callback result.
    MOVD	$cbargs-(18*8+callbackArgs__size)(SP), R13
    MOVD	callbackArgs_result(R13), R0

    //RESTORE_R19_TO_R28(8*9)

    RET
