package objc

import (
	"github.com/ebiten/purego"
	"unsafe"
)

var (
	objc = purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL)

	MsgSend                = purego.Dlsym(objc, "objc_msgSend")
	sel_registerName       = purego.Dlsym(objc, "sel_registerName")
	objc_getClass          = purego.Dlsym(objc, "objc_getClass")
	objc_allocateClassPair = purego.Dlsym(objc, "objc_allocateClassPair")
	objc_registerClassPair = purego.Dlsym(objc, "objc_registerClassPair")
	class_addMethod        = purego.Dlsym(objc, "class_addMethod")
)

func Send(cls Class, sel SEL, args ...uintptr) uintptr {
	var tmp = make([]uintptr, len(args)+2)
	tmp[0] = uintptr(cls)
	tmp[1] = uintptr(sel)
	copy(tmp[2:], args)
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

type _IMP uintptr

// IMP takes a go function and returns an IMP that can be called by C code
func IMP(fn interface{}) _IMP {
	if x, ok := fn.(uintptr); ok {
		return _IMP(x)
	}
	return 0
	//val := reflect.ValueOf(fn)
	//if val.Kind() != reflect.Func {
	//	panic("not a function")
	//}
	//return _IMP(val.Pointer())
}
