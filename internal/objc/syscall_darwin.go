package objc

import (
	"reflect"
	"runtime"
	"sync"
	"unsafe"
)

func NewCallback(fn interface{}) uintptr {
	return compileCallback(fn)
}

// only increase this if you have added more to the callbackasm function
const maxCB = 2000 // maximum number of callbacks

var cbs struct {
	lock  sync.Mutex
	numFn int                  // the number of functions currently in cbs.funcs
	funcs [maxCB]reflect.Value // the saved callbacks
}

type callbackArgs struct {
	index uintptr
	// args points to the argument block.
	//
	// For cdecl and stdcall, all arguments are on the stack.
	//
	// For fastcall, the trampoline spills register arguments to
	// the reserved spill slots below the stack arguments,
	// resulting in a layout equivalent to stdcall.
	//
	// For arm, the trampoline stores the register arguments just
	// below the stack arguments, so again we can treat it as one
	// big stack arguments frame.
	args unsafe.Pointer
	// Below are out-args from callbackWrap
	result uintptr
}

func compileCallback(fn interface{}) uintptr {
	val := reflect.ValueOf(fn)
	if val.Kind() != reflect.Func {
		panic("type is not a function")
	}
	if cbs.numFn >= maxCB {
		panic("the maximum number of callbacks has been reached")
	}
	ty := val.Type()
	for i := 0; i < ty.NumIn(); i++ {
		in := ty.In(i)
		switch in.Kind() {
		case reflect.Struct, reflect.Float32, reflect.Float64,
			reflect.Interface, reflect.Func, reflect.Slice, reflect.Chan, reflect.Complex128, reflect.Complex64:
			panic("unsupported argument type: " + in.Kind().String())
		}
	}
	//TODO: windows.NewCallback _requires_ exactly 1 pointer sized argument. Should we too?
	if ty.NumOut() > 1 {
		panic("callbacks can only have one pointer sized return")
	}
	(&cbs.lock).Lock()
	defer (&cbs.lock).Unlock()
	cbs.funcs[cbs.numFn] = val
	cbs.numFn++
	return callbackasmAddr(cbs.numFn - 1)
}

const ptrSize = unsafe.Sizeof((*int)(nil))

const callbackMaxFrame = 64 * ptrSize

var callbackasmABI0 uintptr

func callbackasm()
func callbackasm1()

func callbackWrap(a *callbackArgs) {
	(&cbs.lock).Lock()
	fn := cbs.funcs[a.index]
	(&cbs.lock).Unlock()
	fnType := fn.Type()
	args := make([]reflect.Value, fnType.NumIn())
	frame := (*[callbackMaxFrame]uintptr)(a.args)
	for i := range args {
		//TODO: support float32 and float64
		args[i] = reflect.NewAt(fnType.In(i), unsafe.Pointer(&frame[i])).Elem()
	}
	ret := fn.Call(args)
	if len(ret) > 0 {
		a.result = uintptr(ret[0].Uint())
	}
}

// callbackasmAddr returns address of runtime.callbackasm
// function adjusted by i.
// On x86 and amd64, runtime.callbackasm is a series of CALL instructions,
// and we want callback to arrive at
// correspondent call instruction instead of start of
// runtime.callbackasm.
// On ARM, runtime.callbackasm is a series of mov and branch instructions.
// R12 is loaded with the callback index. Each entry is two instructions,
// hence 8 bytes.
func callbackasmAddr(i int) uintptr {
	var entrySize int
	switch runtime.GOARCH {
	default:
		panic("unsupported architecture")
	case "386", "amd64":
		entrySize = 5
	case "arm", "arm64":
		// On ARM and ARM64, each entry is a MOV instruction
		// followed by a branch instruction
		entrySize = 8
	}
	return callbackasmABI0 + uintptr(i*entrySize)
}
