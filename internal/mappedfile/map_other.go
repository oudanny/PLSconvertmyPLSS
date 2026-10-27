//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package mappedfile

import (
	"errors"
	"os"
)

func mapReadOnly(f *os.File, size int) ([]byte, func() error, error) {
	return nil, nil, errors.New("read-only mapping unsupported on this platform")
}
