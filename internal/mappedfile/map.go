// Package mappedfile provides an experimental read-only file mapping. It never
// writes the mapped file. Users must stop accessing Bytes before Close.
package mappedfile

import (
	"errors"
	"os"
)

type Mapping struct {
	Bytes    []byte
	file     *os.File
	release  func() error
	ownsFile bool
}

func Open(path string) (*Mapping, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	m, e := FromFile(f)
	if e != nil {
		f.Close()
		return nil, e
	}
	m.ownsFile = true
	return m, nil
}

// FromFile maps the already-open handle, avoiding a path-replacement race.
// The caller owns f and must keep it unchanged until the mapping is closed.
func FromFile(f *os.File) (*Mapping, error) {
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if info.Size() <= 0 || uint64(info.Size()) > uint64(^uint(0)>>1) {
		return nil, errors.New("file size cannot be mapped")
	}
	data, release, e := mapReadOnly(f, int(info.Size()))
	if e != nil {
		return nil, e
	}
	return &Mapping{Bytes: data, file: f, release: release}, nil
}
func (m *Mapping) Close() error {
	if m.file == nil {
		return nil
	}
	e := m.release()
	var closeErr error
	if m.ownsFile {
		closeErr = m.file.Close()
	}
	m.file = nil
	m.Bytes = nil
	if e != nil {
		return e
	}
	return closeErr
}
