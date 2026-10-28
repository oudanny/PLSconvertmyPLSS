// Package regionatlas stores exact per-state atlases in one offline file.
// It maps the container once and keeps a bounded, lazy cache of state readers.
package regionatlas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/mappedfile"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const headerSize = 65536
const maxShards = 64
const magic = "PLSSREG1"

type ShardInput struct {
	Path string
	// State is an optional assertion against the embedded source metadata.
	State string
}

type Shard struct {
	State        string              `json:"state"`
	Bounds       spatialindex.Bounds `json:"bounds"`
	HasGeometry  bool                `json:"has_geometry"`
	Prefixes     []string            `json:"id_prefixes"`
	Section      atlas.Section       `json:"section"`
	Source       plss.PackMetadata   `json:"source"`
	SourceSHA256 string              `json:"source_sha256"`
}
type Manifest struct {
	Format string  `json:"format"`
	Shards []Shard `json:"shards"`
}

func stateValid(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

func prefixes(a *atlas.Reader) []string {
	return auditPrefixes(a.Keys.RoutingPrefixes(), a.Manifest.Unlocated)
}

func auditPrefixes(keys []string, audit []plss.UnlocatedRecord) []string {
	seen := map[string]bool{}
	for _, p := range keys {
		seen[p] = true
	}
	for _, r := range audit {
		for _, field := range []string{"PLSSID", "FRSTDIVID", "SECDIVID", "SURVID"} {
			if id, ok := r.Attributes[field].(string); ok && id != "" {
				seen[keyindex.RoutingPrefix(id)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Build copies each complete, verified state atlas byte for byte. It refuses
// existing destinations. Input files must remain unchanged during the build.
// A failed build removes its own staging file; no source is modified.
func Build(out string, inputs []ShardInput) (Manifest, error) {
	return BuildContext(context.Background(), out, inputs)
}

// BuildContext stages in the destination directory and publishes a complete,
// synced file with an atomic no-overwrite link. Cancellation is checked during
// copying and verification, and immediately before publication. An interrupted
// process can leave a staging file, but never an incomplete final destination.
func BuildContext(ctx context.Context, out string, inputs []ShardInput) (Manifest, error) {
	m := Manifest{Format: magic}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	if len(inputs) == 0 || len(inputs) > maxShards {
		return m, errors.New("bundle requires 1–64 state atlases")
	}
	if _, err := os.Lstat(out); err == nil {
		return m, &os.PathError{Op: "create", Path: out, Err: os.ErrExist}
	} else if !errors.Is(err, os.ErrNotExist) {
		return m, err
	}
	f, err := os.CreateTemp(filepath.Dir(out), ".plss-region-*.partial")
	if err != nil {
		return m, err
	}
	staging := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(staging)
	}()
	if _, err := f.Write(make([]byte, headerSize)); err != nil {
		return m, err
	}
	seen := map[string]bool{}
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		s, err := appendShard(ctx, f, input)
		if err != nil {
			return m, fmt.Errorf("bundle %s: %w", input.Path, err)
		}
		if seen[s.State] {
			return m, fmt.Errorf("duplicate state %s", s.State)
		}
		seen[s.State] = true
		m.Shards = append(m.Shards, s)
	}
	doc, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	if len(doc) > headerSize-44 {
		return m, errors.New("region manifest exceeds 64 KiB")
	}
	header := make([]byte, headerSize)
	copy(header, magic)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(doc)))
	h := sha256.Sum256(doc)
	copy(header[12:44], h[:])
	copy(header[44:], doc)
	if _, err := f.WriteAt(header, 0); err != nil {
		return m, err
	}
	if err := f.Sync(); err != nil {
		return m, err
	}
	if err := f.Close(); err != nil {
		return m, err
	}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	if err := os.Link(staging, out); err != nil {
		return m, fmt.Errorf("publish region without overwriting: %w", err)
	}
	return m, nil
}

func appendShard(ctx context.Context, out *os.File, input ShardInput) (Shard, error) {
	var s Shard
	in, err := os.Open(input.Path)
	if err != nil {
		return s, err
	}
	defer in.Close()
	mapping, err := mappedfile.FromFile(in)
	if err != nil {
		return s, err
	}
	defer mapping.Close()
	a, err := atlas.OpenView(mapping.Bytes)
	if err != nil {
		return s, err
	}
	defer a.Close()
	if err := a.VerifyContext(ctx); err != nil {
		return s, err
	}
	s.State = a.Manifest.Source.State
	if !stateValid(s.State) || (input.State != "" && input.State != s.State) {
		return s, errors.New("state assertion disagrees with atlas source")
	}
	if err := validateCounts(a); err != nil {
		return s, err
	}
	s.Bounds, s.HasGeometry, err = a.Bounds()
	if err != nil {
		return s, err
	}
	s.Prefixes = prefixes(a)
	s.Source, s.SourceSHA256 = a.Manifest.Source, a.Manifest.SourceSHA256
	s.Section.Offset, err = out.Seek(0, io.SeekCurrent)
	if err != nil {
		return s, err
	}
	h := sha256.New()
	s.Section.Size, err = io.Copy(io.MultiWriter(out, h), contextReader{ctx: ctx, reader: bytes.NewReader(mapping.Bytes)})
	if err != nil {
		return s, err
	}
	s.Section.SHA256 = hex.EncodeToString(h.Sum(nil))
	return s, nil
}

func validateCounts(a *atlas.Reader) error {
	return validateMetadataCounts(a.Manifest.Source, a.Keys.LayerCounts(), a.Manifest.Unlocated)
}

func validateMetadataCounts(source plss.PackMetadata, layers [3]uint32, audit []plss.UnlocatedRecord) error {
	names := []string{"township", "section", "intersected"}
	known := func(name string) bool { return name == "township" || name == "section" || name == "intersected" }
	for name, n := range source.Counts {
		if !known(name) || n < 0 {
			return errors.New("invalid source layer count")
		}
	}
	for name, n := range source.UnlocatedCounts {
		if !known(name) || n < 0 {
			return errors.New("invalid unlocated layer count")
		}
	}
	for i, name := range names {
		if uint64(source.Counts[name]) != uint64(layers[i]) {
			return errors.New("source layer count disagrees with key header")
		}
	}
	counts := map[string]int{}
	for _, record := range audit {
		if !known(record.Layer) {
			return errors.New("unknown unlocated layer")
		}
		counts[record.Layer]++
	}
	for _, name := range names {
		if counts[name] != source.UnlocatedCounts[name] {
			return errors.New("unlocated source count disagrees with audit records")
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type Options struct {
	// Buffered uses bounded ordinary reads instead of one native mapping.
	Buffered bool
	// MaxOpenShards limits decoded state indexes and caches. Zero keeps all
	// requested shards (at most 64) so repeated cross-state queries stay warm.
	MaxOpenShards int
}

type cached struct {
	reader *atlas.Reader
	refs   int
	used   uint64
}

type Reader struct {
	mu         sync.RWMutex
	closed     bool
	file       *os.File
	mapping    *mappedfile.Mapping
	source     io.ReaderAt
	manifest   Manifest
	standalone bool
	byState    map[string]int
	filters    map[int]*keyindex.IDFilter
	audits     map[int][]plss.UnlocatedRecord
	cacheMu    sync.Mutex
	cache      map[int]*cached
	changed    chan struct{}
	tick       uint64
	maxOpen    int
}

// Open uses ordinary file reads. OpenMapped is the preferred desktop path.
func Open(path string) (*Reader, error)       { return OpenWithOptions(path, Options{Buffered: true}) }
func OpenMapped(path string) (*Reader, error) { return OpenWithOptions(path, Options{}) }

// OpenWithOptions accepts either a single state atlas or a region bundle.
// Opening validates routing/header integrity and independent index extents;
// full component SHA256 verification remains an explicit Verify operation.
func OpenWithOptions(path string, options Options) (*Reader, error) {
	r, err := newReader(options)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r.file, r.source = f, f
	fail := func(err error) (*Reader, error) { _ = r.Close(); return nil, err }
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !options.Buffered {
		r.mapping, err = mappedfile.FromFile(f)
		if err != nil {
			return fail(err)
		}
		r.source = bytes.NewReader(r.mapping.Bytes)
	}
	if err := r.initialize(info.Size()); err != nil {
		return fail(err)
	}
	return r, nil
}

// OpenReaderAt borrows an immutable random-access source. It never closes the
// source or maps/copies the whole container. Individual state indexes are read
// on demand and bounded by MaxOpenShards; geometry blocks have bounded caches.
// The caller must keep the source available until Close returns. Offsets and
// total file size use int64, independently of the host's pointer width.
func OpenReaderAt(source io.ReaderAt, size int64, options Options) (*Reader, error) {
	if source == nil {
		return nil, errors.New("nil atlas source")
	}
	r, err := newReader(options)
	if err != nil {
		return nil, err
	}
	r.source = source
	if err := r.initialize(size); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func newReader(options Options) (*Reader, error) {
	if options.MaxOpenShards == 0 {
		options.MaxOpenShards = maxShards
	}
	if options.MaxOpenShards < 1 || options.MaxOpenShards > maxShards {
		return nil, errors.New("MaxOpenShards must be 1-64")
	}
	return &Reader{cache: map[int]*cached{}, byState: map[string]int{}, filters: map[int]*keyindex.IDFilter{}, audits: map[int][]plss.UnlocatedRecord{}, changed: make(chan struct{}), maxOpen: options.MaxOpenShards}, nil
}

func (r *Reader) initialize(size int64) error {
	if size < headerSize {
		return errors.New("truncated atlas or region header")
	}
	h := make([]byte, headerSize)
	if _, err := r.source.ReadAt(h, 0); err != nil {
		return err
	}
	if string(h[:8]) == "PLSSATL1" {
		m, err := atlas.ReadManifest(r.source, size)
		if err != nil {
			return err
		}
		bounds, has, err := atlas.ReadBounds(r.source, m)
		if err != nil {
			return err
		}
		keys := m.Sections["keys"]
		filter, err := keyindex.ReadIDFilter(io.NewSectionReader(r.source, keys.Offset, keys.Size), keys.Size)
		if err != nil {
			return err
		}
		if err := validateMetadataCounts(m.Source, filter.LayerCounts(), m.Unlocated); err != nil {
			return err
		}
		r.filters[0], r.audits[0] = filter, m.Unlocated
		r.standalone = true
		r.manifest = Manifest{Format: "PLSSATL1", Shards: []Shard{{State: m.Source.State, Bounds: bounds, HasGeometry: has, Prefixes: []string{"*"}, Section: atlas.Section{Size: size}, Source: m.Source, SourceSHA256: m.SourceSHA256}}}
	} else {
		if string(h[:8]) != magic {
			return errors.New("not a completed atlas or region bundle")
		}
		length := binary.LittleEndian.Uint32(h[8:12])
		if length == 0 || length > headerSize-44 {
			return errors.New("invalid region manifest length")
		}
		n := int(length)
		doc := h[44 : 44+n]
		digest := sha256.Sum256(doc)
		if !bytes.Equal(h[12:44], digest[:]) {
			return errors.New("region manifest checksum mismatch")
		}
		if err := json.Unmarshal(doc, &r.manifest); err != nil {
			return err
		}
		if r.manifest.Format != magic || len(r.manifest.Shards) == 0 || len(r.manifest.Shards) > maxShards {
			return errors.New("invalid region format or shard count")
		}
		last := int64(headerSize)
		for shardIndex, s := range r.manifest.Shards {
			if !stateValid(s.State) || s.Source.State != s.State || s.Section.Offset != last || s.Section.Size < headerSize || s.Section.Size > size-last {
				return errors.New("invalid state shard directory")
			}
			if hash, e := hex.DecodeString(s.Section.SHA256); e != nil || len(hash) != sha256.Size {
				return errors.New("invalid shard checksum")
			}
			for i, p := range s.Prefixes {
				if (p != "*" && !stateValid(p)) || (i > 0 && p <= s.Prefixes[i-1]) {
					return errors.New("invalid shard ID routes")
				}
			}
			section := io.NewSectionReader(r.source, s.Section.Offset, s.Section.Size)
			m, e := atlas.ReadManifest(section, s.Section.Size)
			if e != nil {
				return e
			}
			bounds, has, e := atlas.ReadBounds(section, m)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(m.Source, s.Source) || m.SourceSHA256 != s.SourceSHA256 || bounds != s.Bounds || has != s.HasGeometry {
				return errors.New("shard provenance or extent disagrees with region manifest")
			}
			keys := m.Sections["keys"]
			filter, e := keyindex.ReadIDFilter(io.NewSectionReader(section, keys.Offset, keys.Size), keys.Size)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(auditPrefixes(filter.Prefixes(), m.Unlocated), s.Prefixes) {
				return errors.New("shard ID routes disagree with key index")
			}
			if err := validateMetadataCounts(m.Source, filter.LayerCounts(), m.Unlocated); err != nil {
				return err
			}
			r.filters[shardIndex], r.audits[shardIndex] = filter, m.Unlocated
			last += s.Section.Size
		}
		if last != size {
			return errors.New("unexpected region file length")
		}
	}
	for i, s := range r.manifest.Shards {
		if _, ok := r.byState[s.State]; ok {
			return errors.New("duplicate region state")
		}
		r.byState[s.State] = i
	}
	return nil
}

// acquire is called with the lifetime read lock held. Idle readers can be
// evicted, but active ones are pinned. Contention waits without exceeding the
// configured bound, and canceled lookups can abandon the wait.
func (r *Reader) acquire(ctx context.Context, index int) (*atlas.Reader, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r.cacheMu.Lock()
		r.tick++
		if c := r.cache[index]; c != nil {
			c.refs++
			c.used = r.tick
			r.cacheMu.Unlock()
			return c.reader, func() { r.release(index) }, nil
		}
		if len(r.cache) >= r.maxOpen {
			victim, oldest := -1, ^uint64(0)
			for id, c := range r.cache {
				if c.refs == 0 && c.used < oldest {
					victim, oldest = id, c.used
				}
			}
			if victim < 0 {
				changed := r.changed
				r.cacheMu.Unlock()
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-changed:
					continue
				}
			}
			_ = r.cache[victim].reader.Close()
			delete(r.cache, victim)
		}
		s := r.manifest.Shards[index]
		var a *atlas.Reader
		var err error
		if r.mapping != nil {
			a, err = atlas.OpenView(r.mapping.Bytes[s.Section.Offset : s.Section.Offset+s.Section.Size])
		} else {
			a, err = atlas.OpenReaderAt(io.NewSectionReader(r.source, s.Section.Offset, s.Section.Size), s.Section.Size)
		}
		if err == nil && !r.standalone && !reflect.DeepEqual(prefixes(a), s.Prefixes) {
			_ = a.Close()
			err = errors.New("shard ID routes disagree with key index")
		}
		if err == nil && !r.standalone {
			if countErr := validateCounts(a); countErr != nil {
				_ = a.Close()
				err = countErr
			}
		}
		if err != nil {
			r.cacheMu.Unlock()
			return nil, nil, fmt.Errorf("open %s shard: %w", s.State, err)
		}
		r.cache[index] = &cached{reader: a, refs: 1, used: r.tick}
		r.cacheMu.Unlock()
		return a, func() { r.release(index) }, nil
	}
}

func (r *Reader) release(index int) {
	r.cacheMu.Lock()
	r.cache[index].refs--
	close(r.changed)
	r.changed = make(chan struct{})
	r.cacheMu.Unlock()
}

func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	for _, c := range r.cache {
		errs = append(errs, c.reader.Close())
	}
	r.cache = nil
	r.filters = nil
	r.audits = nil
	if r.mapping != nil {
		errs = append(errs, r.mapping.Close())
	}
	if r.file != nil {
		errs = append(errs, r.file.Close())
	}
	return errors.Join(errs...)
}

type Hit struct {
	atlas.Hit
	State string `json:"state"`
}

func rank(layer string) int {
	return map[string]int{"township": 1, "section": 2, "intersected": 3}[layer]
}

func (r *Reader) Lookup(ctx context.Context, lat, lon float64) ([]Hit, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, os.ErrClosed
	}
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, errors.New("invalid WGS84 point")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var hits []Hit
	finest := 0
	for i, s := range r.manifest.Shards {
		b := s.Bounds
		if !s.HasGeometry || lon < b.MinLon || lon > b.MaxLon || lat < b.MinLat || lat > b.MaxLat {
			continue
		}
		a, done, err := r.acquire(ctx, i)
		if err != nil {
			return nil, err
		}
		found, err := a.Lookup(ctx, lat, lon)
		done()
		if errors.Is(err, plss.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, h := range found {
			level := rank(h.Layer)
			if level > finest {
				finest = level
				hits = hits[:0]
			}
			if level == finest {
				hits = append(hits, Hit{Hit: h, State: s.State})
			}
		}
	}
	if len(hits) == 0 {
		return nil, plss.ErrNotFound
	}
	return hits, nil
}

func (r *Reader) FindID(kind, id string) ([]Hit, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, os.ErrClosed
	}
	if kind != "township" && kind != "section" && kind != "division" && kind != "intersected" && kind != "survey" {
		return nil, errors.New("invalid identifier kind")
	}
	prefix := keyindex.RoutingPrefix(id)
	var hits []Hit
	unlocated := false
	for i, s := range r.manifest.Shards {
		if prefix != "*" && !contains(s.Prefixes, prefix) && !contains(s.Prefixes, "*") {
			continue
		}
		if auditHasID(r.audits[i], kind, id) {
			unlocated = true
		}
		if filter := r.filters[i]; filter != nil && !filter.MayContain(kind, id) {
			continue
		}
		a, done, err := r.acquire(context.Background(), i)
		if err != nil {
			return nil, err
		}
		found, err := a.FindID(kind, id)
		done()
		if errors.Is(err, plss.ErrNoGeometry) {
			unlocated = true
			continue
		}
		if errors.Is(err, plss.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, h := range found {
			hits = append(hits, Hit{Hit: h, State: s.State})
		}
	}
	if len(hits) > 0 {
		return hits, nil
	}
	if unlocated {
		return nil, fmt.Errorf("%w: %s", plss.ErrNoGeometry, id)
	}
	return nil, plss.ErrNotFound
}

func auditHasID(records []plss.UnlocatedRecord, kind, id string) bool {
	field := map[string]string{"township": "PLSSID", "section": "FRSTDIVID", "division": "SECDIVID", "intersected": "SECDIVID", "survey": "SURVID"}[kind]
	layer := map[string]string{"township": "township", "section": "section", "division": "intersected", "intersected": "intersected", "survey": "intersected"}[kind]
	for _, record := range records {
		if record.Layer == layer && record.Attributes[field] == id {
			return true
		}
	}
	return false
}
func contains(items []string, s string) bool {
	i := sort.SearchStrings(items, s)
	return i < len(items) && items[i] == s
}

func (r *Reader) withHit(hit Hit, fn func(*atlas.Reader) error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return os.ErrClosed
	}
	i, ok := r.byState[hit.State]
	if !ok {
		return errors.New("hit state is not present in this atlas")
	}
	a, done, err := r.acquire(context.Background(), i)
	if err != nil {
		return err
	}
	defer done()
	return fn(a)
}
func (r *Reader) Full(hit Hit) (m plss.Match, err error) {
	err = r.withHit(hit, func(a *atlas.Reader) error { var e error; m, e = a.Full(hit.Hit); return e })
	return
}
func (r *Reader) Representative(hit Hit) (p plss.Point, err error) {
	err = r.withHit(hit, func(a *atlas.Reader) error { var e error; p, e = a.Representative(hit.Hit); return e })
	return
}
func (r *Reader) HasFullAttributes(hit Hit) (has bool, err error) {
	err = r.withHit(hit, func(a *atlas.Reader) error { has = a.HasFullAttributes(); return nil })
	return
}

func (r *Reader) Sources() []plss.PackMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]plss.PackMetadata, len(r.manifest.Shards))
	for i, s := range r.manifest.Shards {
		out[i] = cloneSource(s.Source)
	}
	return out
}
func cloneSource(s plss.PackMetadata) plss.PackMetadata {
	counts := func(in map[string]int) map[string]int {
		if in == nil {
			return nil
		}
		out := make(map[string]int, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	s.Counts, s.UnlocatedCounts = counts(s.Counts), counts(s.UnlocatedCounts)
	return s
}

type UnlocatedRecord struct {
	State string `json:"state"`
	plss.UnlocatedRecord
}

func (r *Reader) Unlocated(ctx context.Context) ([]UnlocatedRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, os.ErrClosed
	}
	var out []UnlocatedRecord
	for _, s := range r.manifest.Shards {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Audit data lives in the small state header. Read it independently so
		// auditing does not initialize state indexes or alias their attributes.
		m, err := atlas.ReadManifest(io.NewSectionReader(r.source, s.Section.Offset, s.Section.Size), s.Section.Size)
		if err != nil {
			return nil, err
		}
		for _, item := range m.Unlocated {
			out = append(out, UnlocatedRecord{State: s.State, UnlocatedRecord: item})
		}
	}
	return out, nil
}

// Verify checks every state component, ID route, and full copied shard hash.
func (r *Reader) Verify() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return os.ErrClosed
	}
	for i, s := range r.manifest.Shards {
		a, done, err := r.acquire(context.Background(), i)
		if err != nil {
			return err
		}
		err = a.Verify()
		done()
		if err != nil {
			return fmt.Errorf("verify %s: %w", s.State, err)
		}
		if !r.standalone {
			h := sha256.New()
			if _, err := io.Copy(h, io.NewSectionReader(r.source, s.Section.Offset, s.Section.Size)); err != nil {
				return err
			}
			if hex.EncodeToString(h.Sum(nil)) != s.Section.SHA256 {
				return fmt.Errorf("%s shard checksum mismatch", s.State)
			}
		}
	}
	return nil
}

// OpenShards reports currently initialized readers, useful for memory audits.
func (r *Reader) OpenShards() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	return len(r.cache)
}
