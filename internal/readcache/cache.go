// Package readcache bounds browser bridge reads and retains a small page cache.
// It uses int64 offsets even on a 32-bit WebAssembly target.
package readcache

import (
	"container/list"
	"errors"
	"io"
	"sync"
)

const PageSize = 64 << 10
const MaxReadSize = 1 << 20
const MaxRangeReadSize = 8 << 20

type Options struct{ Pages, PageSize, MaxReadBytes int }

type Stats struct {
	ReadCalls    uint64 `json:"read_calls"`
	BytesRead    uint64 `json:"bytes_read"`
	MaxReadBytes int    `json:"max_read_bytes"`
	CacheHits    uint64 `json:"cache_hits"`
	CacheBytes   int    `json:"cache_bytes"`
}
type entry struct {
	offset int64
	data   []byte
}
type Reader struct {
	mu       sync.Mutex
	source   io.ReaderAt
	size     int64
	limit    int
	pageSize int
	maxRead  int
	pages    map[int64]*list.Element
	order    *list.List
	stats    Stats
}

func New(source io.ReaderAt, size int64, pages int) (*Reader, error) {
	return NewWithPageSize(source, size, pages, PageSize)
}

// NewWithPageSize allows higher-latency byte-range transports to amortize small
// adjacent reads. Page sizes are bounded by MaxReadSize; retained bytes are at
// most pages*pageSize. Local FileReaderSync uses the smaller default pages.
func NewWithPageSize(source io.ReaderAt, size int64, pages, pageSize int) (*Reader, error) {
	return NewWithOptions(source, size, Options{Pages: pages, PageSize: pageSize, MaxReadBytes: MaxReadSize})
}

// NewWithOptions also controls bulk read size. Larger bounded transfers are
// useful for high-latency HTTP ranges; they do not increase retained page bytes.
func NewWithOptions(source io.ReaderAt, size int64, options Options) (*Reader, error) {
	pages, pageSize, maxRead := options.Pages, options.PageSize, options.MaxReadBytes
	if source == nil || size < 0 || pages < 1 || pages > 1024 || pageSize < 4096 || pageSize > MaxReadSize || pageSize&(pageSize-1) != 0 || maxRead < pageSize || maxRead > MaxRangeReadSize {
		return nil, errors.New("invalid bounded reader options")
	}
	return &Reader{source: source, size: size, limit: pages, pageSize: pageSize, maxRead: maxRead, pages: map[int64]*list.Element{}, order: list.New()}, nil
}
func (r *Reader) Stats() Stats { r.mu.Lock(); defer r.mu.Unlock(); return r.stats }

func (r *Reader) read(p []byte, off int64) (int, error) {
	r.stats.ReadCalls++
	r.stats.MaxReadBytes = max(r.stats.MaxReadBytes, len(p))
	n, err := r.source.ReadAt(p, off)
	r.stats.BytesRead += uint64(n)
	if err == nil && n != len(p) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if off < 0 {
		return 0, errors.New("negative file offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	want := len(p)
	if int64(len(p)) > r.size-off {
		p = p[:int(r.size-off)]
	}
	total := 0
	for len(p) > 0 {
		// Large sequential reads bypass the cache, avoiding duplicate copies of
		// complete state indexes, while each bridge call stays within maxRead.
		if len(p) >= 2*r.pageSize {
			n := min(len(p), r.maxRead)
			got, err := r.read(p[:n], off)
			total += got
			off += int64(got)
			p = p[got:]
			if err != nil {
				return total, err
			}
			continue
		}
		base := off / int64(r.pageSize) * int64(r.pageSize)
		item := r.pages[base]
		if item == nil {
			data := make([]byte, int(min(int64(r.pageSize), r.size-base)))
			if _, err := r.read(data, base); err != nil {
				return total, err
			}
			item = r.order.PushFront(entry{base, data})
			r.pages[base] = item
			r.stats.CacheBytes += len(data)
			if len(r.pages) > r.limit {
				old := r.order.Back()
				e := old.Value.(entry)
				delete(r.pages, e.offset)
				r.order.Remove(old)
				r.stats.CacheBytes -= len(e.data)
			}
		} else {
			r.order.MoveToFront(item)
			r.stats.CacheHits++
		}
		data := item.Value.(entry).data
		n := copy(p, data[int(off-base):])
		total += n
		off += int64(n)
		p = p[n:]
	}
	if total < want {
		return total, io.EOF
	}
	return total, nil
}
