#include "textflag.h"
#include "abi_amd64.h"
#include "go_asm.h"

// _crosscall2 expects a call to the ABIInternal function
// However, the tag <ABIInternal> is only available in the runtime :(
// This is a small wrapper function that moves the parameter from AX to the stack
// where the Go function can find it. It then calls callbackWrap
TEXT callbackWrapInternal(SB), $0-0
	MOVQ AX, 0(SP)
	CALL ·callbackWrap(SB)
	RET

TEXT runtime·callbackasm1(SB), NOSPLIT, $0
	// Construct args vector for cgocallback().
	// By windows/amd64 calling convention first 4 args are in CX, DX, R8, R9
	// args from the 5th on are on the stack.
	// In any case, even if function has 0,1,2,3,4 args, there is reserved
	// but uninitialized "shadow space" for the first 4 args.
	// The values are in registers.
	MOVQ CX, (16+0)(SP)
	MOVQ DX, (16+8)(SP)
	MOVQ R8, (16+16)(SP)
	MOVQ R9, (16+24)(SP)

	// R8 = address of args vector
	LEAQ (16+0)(SP), R8

	// remove return address from stack, we are not returning to callbackasm, but to its caller.
	MOVQ 0(SP), AX
	ADDQ $8, SP

	// determine index into runtime·cbs table
	MOVQ $runtime·callbackasm(SB), DX
	SUBQ DX, AX
	MOVQ $0, DX
	MOVQ $5, CX                       // divide by 5 because each call instruction in runtime·callbacks is 5 bytes long
	DIVL CX
	SUBQ $1, AX                       // subtract 1 because return PC is to the next slot

	// Switch from the host ABI to the Go ABI.
	PUSH_REGS_HOST_TO_ABI0()

	// Create a struct callbackArgs on our stack to be passed as
	// the "frame" to cgocallback and on to callbackWrap.
	SUBQ $(24+callbackArgs__size), SP
	MOVQ AX, (24+callbackArgs_index)(SP)  // callback index
	MOVQ R8, (24+callbackArgs_args)(SP)   // address of args vector
	MOVQ $0, (24+callbackArgs_result)(SP) // result
	LEAQ 24(SP), AX

	// Call cgocallback, which will call callbackWrap(frame).
	MOVQ $0, 16(SP)                         // context
	MOVQ AX, 8(SP)                          // frame (address of callbackArgs)
	LEAQ ·callbackWrap<ABIInternal>(SB), BX // cgocallback takes an ABIInternal entry-point
	MOVQ BX, 0(SP)                          // PC of function value to call (callbackWrap)
	CALL ·cgocallback(SB)

	// Get callback result.
	MOVQ (24+callbackArgs_result)(SP), AX
	ADDQ $(24+callbackArgs__size), SP

	POP_REGS_HOST_TO_ABI0()

	// The return value was placed in AX above.
	RET
