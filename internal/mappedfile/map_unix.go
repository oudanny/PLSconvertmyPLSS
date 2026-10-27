//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package mappedfile

import (
	"golang.org/x/sys/unix"
	"os"
)

func mapReadOnly(f *os.File, size int) ([]byte, func() error, error) {
	data, e := unix.Mmap(int(f.Fd()), 0, size, unix.PROT_READ, unix.MAP_SHARED)
	if e != nil {
		return nil, nil, e
	}
	return data, func() error { return unix.Munmap(data) }, nil
}
