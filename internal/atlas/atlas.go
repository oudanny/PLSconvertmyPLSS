// Package atlas combines the experimental compact components in one file.
// It is a research format, not the stable plss.Pack API or a supported download.
package atlas

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
	"sort"
	"sync"

	"github.com/oudanny/PLSconvertmyPLSS/internal/columns"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/mappedfile"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const headerSize = 65536

type Section struct {
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Format       string                 `json:"format"`
	Source       plss.PackMetadata      `json:"source"`
	Sections     map[string]Section     `json:"sections"`
	SourceSHA256 string                 `json:"source_sha256"`
	Unlocated    []plss.UnlocatedRecord `json:"unlocated,omitempty"`
}

func Build(out string, source plss.PackMetadata, sourceHash string, paths map[string]string, audit []plss.UnlocatedRecord) (Manifest, error) {
	m := Manifest{Format: "PLSSATL1", Source: source, SourceSHA256: sourceHash, Sections: map[string]Section{}, Unlocated: audit}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return m, err
	}
	defer f.Close()
	// An interrupted file has a zero magic and is never accepted by Open.
	if _, err = f.Write(make([]byte, headerSize)); err != nil {
		return m, err
	}
	for _, name := range []string{"keys", "index", "geometry", "attributes"} {
		if name == "attributes" && paths[name] == "" {
			continue
		}
		in, err := os.Open(paths[name])
		if err != nil {
			return m, err
		}
		offset, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			in.Close()
			return m, err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, h), in)
		in.Close()
		if err != nil {
			return m, err
		}
		m.Sections[name] = Section{Offset: offset, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	doc, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	if len(doc) > headerSize-12 {
		return m, errors.New("atlas manifest too large")
	}
	header := make([]byte, headerSize)
	copy(header, "PLSSATL1")
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(doc)))
	copy(header[12:], doc)
	if _, err = f.WriteAt(header, 0); err != nil {
		return m, err
	}
	if err = f.Sync(); err != nil {
		return m, err
	}
	return m, nil
}

type Reader struct {
	mu         sync.RWMutex
	closed     bool
	mapping    *mappedfile.Mapping
	view       *spatialindex.View
	File       *os.File
	Manifest   Manifest
	Keys       *keyindex.Index
	Index      *spatialindex.Index
	Geometry   *compactgeom.Reader
	Attributes *columns.Reader
}

func Open(path string) (*Reader, error) { return open(path, false) }

// OpenMapped borrows read-only OS-mapped pages and decodes index leaves lazily.
// Close waits for this reader's methods to finish before unmapping. Component
// pointers must not be used after Close, or concurrently with it.
func OpenMapped(path string) (*Reader, error) { return open(path, true) }

func open(path string, mapped bool) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	a := &Reader{File: f}
	fail := func(err error) (*Reader, error) {
		if a.mapping != nil {
			a.mapping.Close()
		}
		f.Close()
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if info.Size() < headerSize {
		return fail(errors.New("truncated atlas header"))
	}
	var source io.ReaderAt = f
	if mapped {
		a.mapping, err = mappedfile.FromFile(f)
		if err != nil {
			return fail(err)
		}
		source = bytes.NewReader(a.mapping.Bytes)
	}
	header := make([]byte, headerSize)
	if _, err = f.ReadAt(header, 0); err != nil {
		return fail(err)
	}
	if string(header[:8]) != "PLSSATL1" {
		return fail(errors.New("not a completed experimental atlas"))
	}
	n := int(binary.LittleEndian.Uint32(header[8:12]))
	if n > headerSize-12 {
		return fail(errors.New("bad atlas manifest length"))
	}
	decoder := json.NewDecoder(bytes.NewReader(header[12 : 12+n]))
	decoder.UseNumber()
	if err = decoder.Decode(&a.Manifest); err != nil {
		return fail(err)
	}
	if a.Manifest.Format != "PLSSATL1" {
		return fail(errors.New("unsupported atlas format"))
	}
	last := int64(headerSize)
	for _, name := range []string{"keys", "index", "geometry", "attributes"} {
		s, ok := a.Manifest.Sections[name]
		if name == "attributes" && !ok {
			continue
		}
		if !ok || s.Offset != last || s.Size <= 0 || s.Size > info.Size()-last {
			return fail(fmt.Errorf("invalid %s section", name))
		}
		last += s.Size
	}
	if last != info.Size() {
		return fail(errors.New("unexpected atlas length"))
	}
	section := func(name string) (*io.SectionReader, int64) {
		s := a.Manifest.Sections[name]
		return io.NewSectionReader(source, s.Offset, s.Size), s.Size
	}
	r, size := section("keys")
	if mapped {
		s := a.Manifest.Sections["keys"]
		a.Keys, err = keyindex.OpenView(a.mapping.Bytes[s.Offset : s.Offset+s.Size])
	} else {
		a.Keys, err = keyindex.Open(r, size)
	}
	if err != nil {
		return fail(err)
	}
	if mapped {
		s := a.Manifest.Sections["index"]
		a.view, err = spatialindex.OpenView(a.mapping.Bytes[s.Offset : s.Offset+s.Size])
	} else {
		r, size = section("index")
		a.Index, err = spatialindex.Open(r, 0, size)
	}
	if err != nil {
		return fail(err)
	}
	r, size = section("geometry")
	if mapped {
		a.Geometry, err = compactgeom.OpenReaderAtWithOptions(r, size, compactgeom.ReaderOptions{VertexCachePages: 0, GeometryCacheBlocks: 16})
	} else {
		a.Geometry, err = compactgeom.OpenReaderAt(r, size)
	}
	if err != nil {
		return fail(err)
	}
	if _, ok := a.Manifest.Sections["attributes"]; ok {
		r, size = section("attributes")
		a.Attributes, err = columns.OpenReader(r, size)
		if err != nil {
			return fail(err)
		}
	}
	if a.Geometry.Metadata().Decimals != 0 {
		return fail(errors.New("atlas lookup requires exact geometry; rounded experiments are not accepted"))
	}
	nrecords := uint64(a.Keys.Count())
	if uint64(a.Geometry.Metadata().Features) != nrecords || (a.Attributes != nil && a.Attributes.Metadata().Records != nrecords) {
		return fail(errors.New("component feature counts disagree"))
	}
	return a, nil
}

func (a *Reader) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.mapping != nil {
		if err := a.mapping.Close(); err != nil {
			a.File.Close()
			return err
		}
	}
	return a.File.Close()
}
func (a *Reader) IndexMemoryBytes() int64 {
	if a.view != nil {
		return a.view.MemoryBytes()
	}
	return a.Index.MemoryBytes()
}
func (a *Reader) Verify() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return os.ErrClosed
	}
	if err := a.Keys.Validate(); err != nil {
		return err
	}
	var coverageErr error
	if a.view != nil {
		if err := a.view.Validate(); err != nil {
			return err
		}
		coverageErr = a.view.ValidateCoverage(a.Keys.Count(), a.Keys.Layer)
	} else {
		coverageErr = a.Index.ValidateCoverage(a.Keys.Count(), a.Keys.Layer)
	}
	if err := coverageErr; err != nil {
		return err
	}
	for name, s := range a.Manifest.Sections {
		h := sha256.New()
		if _, err := io.Copy(h, io.NewSectionReader(a.File, s.Offset, s.Size)); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != s.SHA256 {
			return fmt.Errorf("%s checksum mismatch", name)
		}
	}
	return nil
}

type Hit struct {
	FeatureID uint32 `json:"feature_id"`
	Layer     string `json:"layer"`
	ID        string `json:"id"`
}

func layerName(layer uint8) string {
	switch layer {
	case 1:
		return "township"
	case 2:
		return "section"
	case 3:
		return "intersected"
	}
	return "unknown"
}

// Lookup does not inflate descriptive metadata. It uses conservative spatial
// candidates followed by original full-precision polygon containment.
func (a *Reader) Lookup(ctx context.Context, lat, lon float64) ([]Hit, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, os.ErrClosed
	}
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, errors.New("invalid WGS84 point")
	}
	for layer := uint8(3); layer > 0; layer-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var candidates []spatialindex.Match
		if a.view != nil {
			var err error
			candidates, err = a.view.SearchLayer(lon, lat, layer)
			if err != nil {
				return nil, err
			}
		} else {
			candidates = a.Index.SearchLayer(lon, lat, layer)
		}
		var hits []Hit
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			g, err := a.Geometry.Record(candidate.ID)
			if err != nil {
				return nil, err
			}
			inside, err := g.ContainsPoint(plss.Point{Latitude: lat, Longitude: lon})
			if err != nil {
				return nil, err
			}
			if inside {
				id, err := a.Keys.ID(candidate.ID)
				if err != nil {
					return nil, err
				}
				hits = append(hits, Hit{candidate.ID, layerName(layer), id})
			}
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}
	return nil, plss.ErrNotFound
}

func (a *Reader) FindID(kind, id string) ([]Hit, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, os.ErrClosed
	}
	rows, err := a.Keys.Find(kind, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		field := map[string]string{"township": "PLSSID", "section": "FRSTDIVID", "division": "SECDIVID", "intersected": "SECDIVID", "survey": "SURVID"}[kind]
		for _, r := range a.Manifest.Unlocated {
			if r.Attributes[field] == id {
				return nil, plss.ErrNoGeometry
			}
		}
		return nil, plss.ErrNotFound
	}
	hits := make([]Hit, 0, len(rows))
	for _, row := range rows {
		key, err := a.Keys.ID(row)
		if err != nil {
			return nil, err
		}
		hits = append(hits, Hit{row, layerName(a.Keys.Layer(row)), key})
	}
	return hits, nil
}

// Full loads source attributes and original rings only when requested. The
// representative is computed from the polygon instead of stored for every row.
func (a *Reader) Full(hit Hit) (plss.Match, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return plss.Match{}, os.ErrClosed
	}
	id, err := a.Keys.ID(hit.FeatureID)
	if err != nil {
		return plss.Match{}, err
	}
	hit.ID = id
	hit.Layer = layerName(a.Keys.Layer(hit.FeatureID))
	g, err := a.Geometry.Record(hit.FeatureID)
	if err != nil {
		return plss.Match{}, err
	}
	attrs, err := a.Keys.Fields(hit.FeatureID)
	if a.Attributes != nil {
		attrs, err = a.Attributes.Attributes(hit.FeatureID)
	}
	if err != nil {
		return plss.Match{}, err
	}
	p, err := g.InteriorPoint()
	if err != nil {
		return plss.Match{}, err
	}
	return plss.Match{Layer: hit.Layer, Label: hit.ID, Attributes: attrs, Geometry: g, Representative: p, Approximate: true, Source: a.Manifest.Source.Source}, nil
}

func Keys(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Layer + ":" + h.ID
	}
	sort.Strings(out)
	return out
}
