//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var counter = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceCounter")

// This experiment measures queries serially. Reusing the output word avoids
// counting an escaping heap allocation on every timing call.
var counterValue int64
var counterFrequency = func() int64 {
	var f int64
	ok, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&f)))
	if ok == 0 || f <= 0 {
		panic("high-resolution performance counter unavailable")
	}
	return f
}()

func preciseNow() int64 {
	ok, _, _ := counter.Call(uintptr(unsafe.Pointer(&counterValue)))
	if ok == 0 {
		panic("performance counter failed")
	}
	v := counterValue
	return (v/counterFrequency)*1e9 + (v%counterFrequency)*1e9/counterFrequency
}
