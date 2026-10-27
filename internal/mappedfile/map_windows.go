//go:build windows

package mappedfile

import (
	"golang.org/x/sys/windows"
	"os"
	"reflect"
	"unsafe"
)

func mapReadOnly(f *os.File, size int) ([]byte, func() error, error) {
	h, e := windows.CreateFileMapping(windows.Handle(f.Fd()), nil, windows.PAGE_READONLY, 0, 0, nil)
	if e != nil {
		return nil, nil, e
	}
	addr, e := windows.MapViewOfFile(h, windows.FILE_MAP_READ, 0, 0, uintptr(size))
	windows.CloseHandle(h)
	if e != nil {
		return nil, nil, e
	}
	// The OS owns this address, outside Go's heap. Populate the header of an
	// actual slice (not a standalone SliceHeader); Close releases the view.
	var data []byte
	header := (*reflect.SliceHeader)(unsafe.Pointer(&data))
	header.Data = addr
	header.Len = size
	header.Cap = size
	return data, func() error { return windows.UnmapViewOfFile(addr) }, nil
}
