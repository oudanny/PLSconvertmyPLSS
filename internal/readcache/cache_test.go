package readcache

import (
	"errors"
	"io"
	"testing"
)

type patternSource struct {
	size int64
	max  int
}

func (s *patternSource) ReadAt(p []byte, off int64) (int, error) {
	s.max = max(s.max, len(p))
	if off < 0 || off >= s.size {
		return 0, io.EOF
	}
	n := min(int64(len(p)), s.size-off)
	for i := range p[:int(n)] {
		p[i] = byte((off + int64(i)) % 251)
	}
	if n < int64(len(p)) {
		return int(n), io.EOF
	}
	return len(p), nil
}
func checkPattern(t *testing.T, p []byte, off int64) {
	t.Helper()
	for i, b := range p {
		if b != byte((off+int64(i))%251) {
			t.Fatalf("byte at %d differs", off+int64(i))
		}
	}
}

func TestBoundedReadsAboveFourGiB(t *testing.T) {
	s := &patternSource{size: 6 << 30}
	r, err := New(s, s.size, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		off int64
		n   int
	}{{(5 << 30) + PageSize - 5, 200}, {17, 3*MaxReadSize + 7}, {(5 << 30) + PageSize - 5, 200}, {s.size - 7, 7}} {
		p := make([]byte, test.n)
		n, err := r.ReadAt(p, test.off)
		if err != nil || n != len(p) {
			t.Fatalf("read: %d %v", n, err)
		}
		checkPattern(t, p, test.off)
	}
	stats := r.Stats()
	if s.max > MaxReadSize || stats.MaxReadBytes > MaxReadSize || stats.CacheBytes > 3*PageSize {
		t.Fatalf("unbounded reader: %+v", stats)
	}
	before := stats.ReadCalls
	_, _ = r.ReadAt(make([]byte, 4), s.size-7)
	if r.Stats().ReadCalls != before || r.Stats().CacheHits == 0 {
		t.Fatal("cached read reread the source")
	}
}
func TestReaderAtEndAndValidation(t *testing.T) {
	r, _ := New(&patternSource{size: 100}, 100, 1)
	p := make([]byte, 20)
	n, err := r.ReadAt(p, 90)
	if n != 10 || !errors.Is(err, io.EOF) {
		t.Fatalf("partial end: %d %v", n, err)
	}
	checkPattern(t, p[:n], 90)
	if n, err := r.ReadAt(p, 100); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("EOF")
	}
	if _, err := r.ReadAt(p, -1); err == nil {
		t.Fatal("negative offset")
	}
	if n, err := r.ReadAt(nil, 100); n != 0 || err != nil {
		t.Fatal("zero length")
	}
	if _, err := New(nil, 10, 1); err == nil {
		t.Fatal("nil source")
	}
}

type shortSource struct{}

func (shortSource) ReadAt(p []byte, off int64) (int, error) { return len(p) - 1, nil }
func TestShortReaderRejected(t *testing.T) {
	r, _ := New(shortSource{}, 65536, 1)
	if _, err := r.ReadAt(make([]byte, 4), 0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short source: %v", err)
	}
}

func TestRangePagesAmortizeSmallSequentialReads(t *testing.T) {
	for _, pageSize := range []int{PageSize, 256 << 10, 512 << 10, MaxReadSize} {
		s := &patternSource{size: 5 << 30}
		r, err := NewWithPageSize(s, s.size, 16, pageSize)
		if err != nil {
			t.Fatal(err)
		}
		for off := int64(4 << 30); off < (4<<30)+(2<<20); off += 4096 {
			p := make([]byte, 4096)
			if _, err := r.ReadAt(p, off); err != nil {
				t.Fatal(err)
			}
			checkPattern(t, p, off)
		}
		stats := r.Stats()
		if stats.ReadCalls != uint64((2<<20)/pageSize) || stats.CacheBytes > 16*pageSize || stats.MaxReadBytes > MaxReadSize {
			t.Fatalf("page %d: %+v", pageSize, stats)
		}
	}
	for _, size := range []int{0, 3000, 12345, 2 << 20} {
		if _, err := NewWithPageSize(&patternSource{size: 1 << 20}, 1<<20, 1, size); err == nil {
			t.Fatalf("invalid page size %d", size)
		}
	}
}

func TestBulkRangeReadsHaveAnIndependentBound(t *testing.T) {
	s := &patternSource{size: 8 << 30}
	r, err := NewWithOptions(s, s.size, Options{Pages: 16, PageSize: 512 << 10, MaxReadBytes: MaxRangeReadSize})
	if err != nil {
		t.Fatal(err)
	}
	p := make([]byte, 2*MaxRangeReadSize+12345)
	if n, err := r.ReadAt(p, (5<<30)+99); err != nil || n != len(p) {
		t.Fatalf("bulk read: %d %v", n, err)
	}
	checkPattern(t, p, (5<<30)+99)
	stats := r.Stats()
	if stats.ReadCalls != 3 || stats.MaxReadBytes != MaxRangeReadSize || stats.CacheBytes > 8<<20 {
		t.Fatalf("bulk transport: %+v", stats)
	}
	if _, err := NewWithOptions(s, s.size, Options{Pages: 1, PageSize: PageSize, MaxReadBytes: MaxRangeReadSize + 1}); err == nil {
		t.Fatal("unbounded range size")
	}
}
