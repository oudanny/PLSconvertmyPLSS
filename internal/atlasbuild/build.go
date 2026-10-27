// Package atlasbuild turns a complete source pack into an exact compact atlas.
// It does not fetch data or mutate its source. The destination is published only
// after all geometry and identifier records have been checked against the source.
package atlasbuild

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
	_ "modernc.org/sqlite"
)

type Options struct {
	// CompressedVertices selects lossless block compression when available.
	CompressedVertices bool
	// PackedVertices selects directly addressable lossless XOR bit packing.
	PackedVertices bool
	Progress       func(phase string, completed, total uint32)
}

type Report struct {
	State               string               `json:"state"`
	Records             uint32               `json:"records"`
	Unlocated           int                  `json:"unlocated"`
	SourceBytes         int64                `json:"source_bytes"`
	AtlasBytes          int64                `json:"atlas_bytes"`
	SourceSHA256        string               `json:"source_sha256"`
	GeometrySHA256      string               `json:"decoded_geometry_sha256"`
	Geometry            compactgeom.Metadata `json:"geometry"`
	GeometryVerified    uint32               `json:"geometry_verified"`
	IdentifiersVerified uint32               `json:"identifiers_verified"`
	BuildSeconds        float64              `json:"build_seconds"`
	Source              plss.PackMetadata    `json:"source"`
}

func database(path string) (*sql.DB, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p = filepath.ToSlash(p)
	if p[0] != '/' {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

// GeometryDigest hashes a framed stream of original float64 bits, including
// ring lengths and all additional ordinates. It is independent of storage codec.
func GeometryDigest(h hash.Hash, id uint32, g plss.Geometry) {
	b := make([]byte, 0, 512)
	b = binary.LittleEndian.AppendUint32(b, id)
	b = binary.AppendUvarint(b, uint64(len(g.Rings)))
	for _, ring := range g.Rings {
		b = binary.AppendUvarint(b, uint64(len(ring)))
		for _, v := range ring {
			b = binary.AppendUvarint(b, uint64(len(v)))
			for _, x := range v {
				b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
			}
		}
	}
	h.Write(b)
}

func hashFile(ctx context.Context, path string) (string, int64, error) {
	if err := selfContained(path); err != nil {
		return "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	var n int64
	b := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		k, e := f.Read(b)
		if k > 0 {
			h.Write(b[:k])
			n += int64(k)
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", 0, e
		}
	}
	if err := selfContained(path); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// The provenance contract hashes one immutable SQLite file. WAL/journal state
// is part of a database, so it must never be silently excluded from that hash.
// Check the documented header versions before opening SQLite, avoiding any
// recovery or creation of shared-memory sidecars on a caller's source.
func selfContained(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return fmt.Errorf("source must be a closed self-contained SQLite pack; found %s", path+suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var header [20]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return err
	}
	if string(header[:16]) != "SQLite format 3\x00" {
		return errors.New("source is not a SQLite database")
	}
	if header[18] != 1 || header[19] != 1 {
		return errors.New("source must use rollback journaling, not WAL; provide a closed self-contained source copy")
	}
	return nil
}

// SourceHash identifies an immutable complete source pack for safe reuse.
func SourceHash(ctx context.Context, path string) (string, error) {
	h, _, err := hashFile(ctx, path)
	return h, err
}

// Build creates a new conversion atlas with exact geometry and published IDs.
// Source attributes remain in the original SQLite pack. Both files record their
// provenance. Every completed feature is checked, not just a random sample.
func Build(ctx context.Context, source, out string, opts Options) (report Report, err error) {
	start := time.Now()
	if err = ctx.Err(); err != nil {
		return
	}
	if _, e := os.Lstat(out); e == nil {
		return report, fmt.Errorf("output already exists: %s", out)
	} else if !errors.Is(e, os.ErrNotExist) {
		return report, e
	}
	// Hash before reading any metadata or rows. The second hash below rejects
	// changes during the build, including the metadata-read window.
	report.SourceSHA256, report.SourceBytes, err = hashFile(ctx, source)
	if err != nil {
		return
	}
	p, err := plss.OpenPack(source)
	if err != nil {
		return report, err
	}
	meta, err := p.Metadata()
	if err != nil {
		p.Close()
		return report, err
	}
	audit, err := p.Unlocated(ctx)
	p.Close()
	if err != nil {
		return report, err
	}
	report.State, report.Source, report.Unlocated = meta.State, meta, len(audit)
	var count uint64
	for _, n := range meta.Counts {
		if n < 0 {
			return report, errors.New("negative source count")
		}
		count += uint64(n)
	}
	if count == 0 || count > math.MaxUint32 {
		return report, errors.New("source feature count is empty or too large")
	}
	report.Records = uint32(count)
	if err = os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return
	}
	temp, err := os.MkdirTemp(filepath.Dir(out), ".atlas-build-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(temp)
	paths := map[string]string{"keys": filepath.Join(temp, "keys"), "index": filepath.Join(temp, "index"), "geometry": filepath.Join(temp, "geometry")}
	geom, err := newGeometryBuilder(paths["geometry"], opts.CompressedVertices, opts.PackedVertices)
	if err != nil {
		return report, err
	}
	defer geom.Close()
	keys := keyindex.NewBuilder()
	db, err := database(source)
	if err != nil {
		return report, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT id,layer,plssid,frstdivid,secdivid,survid,data FROM features ORDER BY id")
	if err != nil {
		return report, err
	}
	defer rows.Close()
	wantGeometry := sha256.New()
	var inflater io.ReadCloser
	defer func() {
		if inflater != nil {
			inflater.Close()
		}
	}()
	var raw bytes.Buffer
	var added uint32
	for rows.Next() {
		var r keyindex.Source
		var blob []byte
		if err = rows.Scan(&r.ID, &r.Layer, &r.Township, &r.First, &r.Second, &r.Survey, &blob); err != nil {
			return
		}
		if err = ctx.Err(); err != nil {
			return
		}
		if inflater == nil {
			inflater, err = zlib.NewReader(bytes.NewReader(blob))
		} else {
			err = inflater.(zlib.Resetter).Reset(bytes.NewReader(blob), nil)
		}
		if err != nil {
			return
		}
		raw.Reset()
		if _, err = io.Copy(&raw, io.LimitReader(inflater, (64<<20)+1)); err != nil {
			return
		}
		if raw.Len() > 64<<20 {
			return report, errors.New("source geometry record exceeds limit")
		}
		if err = inflater.Close(); err != nil {
			return
		}
		var record struct {
			Geometry plss.Geometry `json:"geometry"`
		}
		if err = json.Unmarshal(raw.Bytes(), &record); err != nil {
			return
		}
		if err = geom.Add(r.ID, record.Geometry); err != nil {
			return
		}
		if err = keys.Add(r); err != nil {
			return
		}
		GeometryDigest(wantGeometry, r.ID, record.Geometry)
		added++
		if opts.Progress != nil && (added%100000 == 0 || added == report.Records) {
			opts.Progress("encode", added, report.Records)
		}
	}
	if err = rows.Err(); err != nil {
		return
	}
	rows.Close()
	if added != report.Records {
		return report, errors.New("source counts disagree")
	}
	report.Geometry, err = geom.Finish()
	if err != nil {
		return
	}
	f, err := os.OpenFile(paths["keys"], os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return report, err
	}
	_, err = keys.WriteTo(f)
	closeErr := f.Close()
	if err != nil {
		return
	}
	if closeErr != nil {
		return report, closeErr
	}
	keys = nil
	spatialRows, err := db.QueryContext(ctx, "SELECT feature_id,layer,min_lon,max_lon,min_lat,max_lat FROM spatial ORDER BY id")
	if err != nil {
		return report, err
	}
	defer spatialRows.Close()
	entries := make([]spatialindex.Entry, 0, report.Records)
	for spatialRows.Next() {
		var e spatialindex.Entry
		if err = spatialRows.Scan(&e.ID, &e.Layer, &e.MinLon, &e.MaxLon, &e.MinLat, &e.MaxLat); err != nil {
			return
		}
		entries = append(entries, e)
	}
	if err = spatialRows.Err(); err != nil {
		return
	}
	spatialRows.Close()
	if err = ctx.Err(); err != nil {
		return
	}
	idx, err := spatialindex.Build(entries, 16)
	if err != nil {
		return report, err
	}
	entries = nil
	idx, err = idx.Quantize(8)
	if err != nil {
		return report, err
	}
	f, err = os.OpenFile(paths["index"], os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return report, err
	}
	_, err = idx.WriteTo(f)
	closeErr = f.Close()
	if err != nil {
		return
	}
	if closeErr != nil {
		return report, closeErr
	}
	idx = nil
	if opts.Progress != nil {
		opts.Progress("verify", 0, report.Records)
	}
	built := filepath.Join(temp, "complete.atlas")
	if _, err = atlas.Build(built, meta, report.SourceSHA256, paths, audit); err != nil {
		return
	}
	a, err := atlas.OpenMapped(built)
	if err != nil {
		return report, err
	}
	defer a.Close()
	if err = a.Verify(); err != nil {
		return
	}
	actualGeometry := sha256.New()
	for id := uint32(1); id <= report.Records; id++ {
		if err = ctx.Err(); err != nil {
			return
		}
		g, e := a.Geometry.Record(id)
		if e != nil {
			return report, e
		}
		GeometryDigest(actualGeometry, id, g)
		report.GeometryVerified++
		if opts.Progress != nil && (id%100000 == 0 || id == report.Records) {
			opts.Progress("verify", id, report.Records)
		}
	}
	if !bytes.Equal(wantGeometry.Sum(nil), actualGeometry.Sum(nil)) {
		return report, errors.New("decoded geometry differs from source")
	}
	report.GeometrySHA256 = hex.EncodeToString(actualGeometry.Sum(nil))
	keyRows, err := db.QueryContext(ctx, "SELECT id,plssid,frstdivid,secdivid,survid FROM features ORDER BY id")
	if err != nil {
		return report, err
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var id uint32
		var town, first, second, survey string
		if err = keyRows.Scan(&id, &town, &first, &second, &survey); err != nil {
			return
		}
		fields, e := a.Keys.Fields(id)
		if e != nil {
			return report, e
		}
		for field, want := range map[string]string{"PLSSID": town, "FRSTDIVID": first, "SECDIVID": second, "SURVID": survey} {
			got, _ := fields[field].(string)
			if got != want {
				return report, fmt.Errorf("record%d changed %s", id, field)
			}
		}
		report.IdentifiersVerified++
	}
	if err = keyRows.Err(); err != nil {
		return
	}
	keyRows.Close()
	if report.IdentifiersVerified != report.Records {
		return report, errors.New("identifier count mismatch")
	}
	if err = a.Close(); err != nil {
		return
	}
	// The source is immutable during a build. Verify that assumption before
	// publishing rather than assigning an old provenance hash to changed bytes.
	finalHash, _, e := hashFile(ctx, source)
	if e != nil {
		return report, e
	}
	if finalHash != report.SourceSHA256 {
		return report, errors.New("source pack changed during build")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if err = os.Link(built, out); err != nil {
		return report, fmt.Errorf("publish atlas without overwrite: %w", err)
	}
	info, e := os.Stat(out)
	if e != nil {
		return report, e
	}
	report.AtlasBytes = info.Size()
	report.BuildSeconds = time.Since(start).Seconds()
	return report, nil
}
