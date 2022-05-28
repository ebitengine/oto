package objc

import (
	"reflect"
	"strings"
	"unsafe"
)

/*func gostring(c *uint8) string {
	var size = 0
	o := c
	for *o != 0 {
		o = (*uint8)(unsafe.Add(unsafe.Pointer(o), 1))
		size++
	}
	return (string)(unsafe.Slice(c, size))
}*/

func cstring(g string) *uint8 {
	if !strings.HasSuffix(g, "\x00") {
		panic("str argument missing null terminator: " + g)
	}
	return (*uint8)(unsafe.Pointer((*reflect.StringHeader)(unsafe.Pointer(&g)).Data))
}
