package objc

import (
	"fmt"
	"github.com/ebitengine/purego"
	"reflect"
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
	// IMP is stricter than a normal callback
	switch {
	case val.Type().NumIn() < 2:
	case val.Type().In(0).Kind() != reflect.Uintptr:
	case val.Type().In(1).Kind() != reflect.Uintptr:
		panic("IMP must take a (id, SEL) as its first two arguments")
	}
	return _IMP(NewCallback(fn))
}
