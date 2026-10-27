package mappedfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bytes")
	want := bytes.Repeat([]byte("unaligned data\x00"), 1001)
	if e := os.WriteFile(path, want, 0600); e != nil {
		t.Fatal(e)
	}
	m, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(m.Bytes, want) {
		t.Fatal("mapped content changed")
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(got, want) {
		t.Fatal("mapping changed source file")
	}
}
