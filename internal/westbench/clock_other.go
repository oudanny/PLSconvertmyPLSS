//go:build !windows

package main

import "time"

var clockStart = time.Now()

func preciseNow() int64 { return time.Since(clockStart).Nanoseconds() }
