package objc

import (
	"reflect"
	"strings"
	"unsafe"
)

func cstring(g string) *uint8 {
	if !strings.HasSuffix(g, "\x00") {
		panic("str argument missing null terminator: " + g)
	}
	return (*uint8)(unsafe.Pointer((*reflect.StringHeader)(unsafe.Pointer(&g)).Data))
}
