package objc

import (
	"fmt"
	"github.com/ebitengine/purego"
	"reflect"
	"runtime"
	"sync"
	"unsafe"
)

//https://stackoverflow.com/questions/7062599/example-of-how-objective-cs-try-catch-implementation-is-executed-at-runtime

var (
	objc = purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL)

	// MsgSend is the C function pointer to objc_msgSend.
	// You can call the function yourself or use the convenience function Send
	MsgSend                = purego.Dlsym(objc, "objc_msgSend")
	sel_registerName       = purego.Dlsym(objc, "sel_registerName")
	objc_getClass          = purego.Dlsym(objc, "objc_getClass")
	objc_allocateClassPair = purego.Dlsym(objc, "objc_allocateClassPair")
	objc_registerClassPair = purego.Dlsym(objc, "objc_registerClassPair")
	class_addMethod        = purego.Dlsym(objc, "class_addMethod")
)

// Send is a convenience method for sending messages to objects.
func Send(cls Class, sel SEL, args ...interface{}) uintptr {
	var tmp = make([]uintptr, 2, len(args)+2)
	tmp[0] = uintptr(cls)
	tmp[1] = uintptr(sel)
	for _, a := range args {
		switch v := a.(type) {
		case Class:
			tmp = append(tmp, uintptr(v))
		case SEL:
			tmp = append(tmp, uintptr(v))
		case _IMP:
			tmp = append(tmp, uintptr(v))
		case uintptr:
			tmp = append(tmp, v)
		case int:
			tmp = append(tmp, uintptr(v))
		case uint:
			tmp = append(tmp, uintptr(v))
		default:
			panic(fmt.Sprintf("unknown type %T", v))
		}
	}
	ret, _, _ := purego.SyscallN(MsgSend, tmp...)
	return ret
}

type SEL uintptr

func RegisterName(name string) SEL {
	ret, _, _ := purego.SyscallN(sel_registerName, uintptr(unsafe.Pointer(cstring(name))))
	return SEL(ret)
}

type Class uintptr

func GetClass(name string) Class {
	ret, _, _ := purego.SyscallN(objc_getClass, uintptr(unsafe.Pointer(cstring(name))))
	return Class(ret)
}

func AllocateClassPair(super Class, name string, extraBytes uintptr) Class {
	ret, _, _ := purego.SyscallN(objc_allocateClassPair, uintptr(super), uintptr(unsafe.Pointer(cstring(name))), extraBytes)
	return Class(ret)
}

func (c Class) Register() {
	purego.SyscallN(objc_registerClassPair, uintptr(c))
}

func (c Class) AddMethod(name SEL, imp _IMP, types string) bool {
	ret, _, _ := purego.SyscallN(class_addMethod, uintptr(c), uintptr(name), uintptr(imp), uintptr(unsafe.Pointer(cstring(types))))
	return byte(ret) != 0
}

// _IMP is unexported so that the only way to make this type is by providing a Go function and casting
// it with the IMP function
type _IMP uintptr

// IMP takes a Go function that takes (id, SEL) as its first two arguments. It returns an _IMP that can be called by C code
func IMP(fn interface{}) _IMP {
	// this is only here so that it is easier to port C code to Go.
	// this is not guaranteed to be here forever so make sure to port your callbacks to Go
	// If you have a C function pointer cast it to a uintptr before passing it
	// to this function.
	if x, ok := fn.(uintptr); ok {
		return _IMP(x)
	}
	val := reflect.ValueOf(fn)
	if val.Kind() != reflect.Func {
		panic("not a function")
	}
	//if val.NumField() < 2 || val.Field(0).Kind() != reflect.Uintptr && val.Field(1).Kind() != reflect.Uintptr {
	//	panic("IMP must take a (id, SEL) as its first two arguments")
	//}
	return _IMP(compileCallback(fn))
}

// only increase this if you have added more to the callbackasm function
const maxCB = 2000 // maximum number of callbacks

var cbs struct {
	lock  sync.Mutex
	numFn int // the number of functions currently in cbs.funcs
	funcs [maxCB]reflect.Value
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
	(&cbs.lock).Lock()
	defer (&cbs.lock).Unlock()
	if cbs.numFn >= maxCB {
		panic("the maximum number of callbacks has been reached")
	}
	cbs.funcs[cbs.numFn] = val
	cbs.numFn++
	return callbackasmAddr(cbs.numFn - 1)
}

var callbackasmABI0 uintptr

func callbackasm()
func callbackasm1()

func callbackWrap(a *callbackArgs) {
	fmt.Printf("CALLBACK WRAPPER GOT %+v\n", a)
	(&cbs.lock).Lock()
	defer (&cbs.lock).Unlock()
	cbs.funcs[a.index].Call(nil)
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
