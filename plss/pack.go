//go:build !js

package plss

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const packVersion = 1
const packApplicationID = 0x504c5353 // "PLSS"
const packBatchSize = 1000
const maxPackFeatureBytes = 64 << 20

type packFeature struct {
	Attributes map[string]any `json:"attributes"`
	Geometry   Geometry       `json:"geometry"`
}

// PackWriter builds a pack using bounded, prepared transaction batches. It is
// intended for one importing goroutine. The caller publishes the completed
// file atomically after Finish; Close alone leaves an unreadable partial pack.
type PackWriter struct {
	db        *sql.DB
	tx        *sql.Tx
	feature   *sql.Stmt
	bound     *sql.Stmt
	unlocated *sql.Stmt
	metadata  PackMetadata
	nextID    int64
	nextBound int64
	batch     int
	buffer    bytes.Buffer
	zip       *zlib.Writer
	failed    error
	closed    bool
}

func packDSN(path, mode string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(5000)"}}
	if mode == "ro" {
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// CreatePack reserves a new file exclusively and refuses to overwrite anything.
// Metadata Version, Counts and an omitted CreatedAt are filled by the writer.
func CreatePack(path string, metadata PackMetadata) (*PackWriter, error) {
	metadata.State = strings.ToUpper(strings.TrimSpace(metadata.State))
	if !statePattern.MatchString(metadata.State) {
		return nil, errors.New("pack requires a two-letter state")
	}
	if metadata.Version != 0 && metadata.Version != packVersion {
		return nil, fmt.Errorf("unsupported pack version %d", metadata.Version)
	}
	metadata.Version = packVersion
	if metadata.Source == "" {
		metadata.Source = DefaultURL
	}
	if metadata.CreatedAt == "" {
		metadata.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, metadata.CreatedAt); err != nil {
		return nil, fmt.Errorf("pack created_at: %w", err)
	}
	metadata.Counts = map[string]int{"township": 0, "section": 0, "intersected": 0}
	metadata.UnlocatedCounts = map[string]int{"township": 0, "section": 0, "intersected": 0}
	dsn, err := packDSN(path, "rw")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create offline pack: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	w := &PackWriter{db: db, metadata: metadata}
	fail := func(err error) (*PackWriter, error) {
		_ = w.Close()
		return nil, fmt.Errorf("initialize offline pack: %w", err)
	}
	// MEMORY journal avoids accumulating sidecar files in an unpublished build.
	// Transactions still protect logical consistency; a crash can only damage
	// this incomplete file, which OpenPack rejects. Completed packs are synced.
	for _, statement := range []string{
		"PRAGMA page_size=16384", "PRAGMA journal_mode=MEMORY", "PRAGMA synchronous=OFF",
		"PRAGMA cache_size=-32768", "PRAGMA temp_store=FILE",
		fmt.Sprintf("PRAGMA application_id=%d", packApplicationID), fmt.Sprintf("PRAGMA user_version=%d", packVersion),
		`CREATE TABLE pack_metadata (id INTEGER PRIMARY KEY CHECK(id=1), document TEXT NOT NULL, complete INTEGER NOT NULL CHECK(complete IN (0,1)))`,
		`CREATE TABLE features (
		id INTEGER PRIMARY KEY, layer INTEGER NOT NULL CHECK(layer BETWEEN 1 AND 3),
		plssid TEXT NOT NULL, frstdivid TEXT NOT NULL, secdivid TEXT NOT NULL, survid TEXT NOT NULL,
		stateabbr TEXT NOT NULL, prinmercd TEXT NOT NULL, twnshpno TEXT NOT NULL, twnshpdir TEXT NOT NULL, twnshpfrac TEXT NOT NULL,
		rangeno TEXT NOT NULL, rangedir TEXT NOT NULL, rangefrac TEXT NOT NULL,
		frstdivtyp TEXT NOT NULL, frstdivno TEXT NOT NULL, secdivtyp TEXT NOT NULL, secdivno TEXT NOT NULL,
		qsec TEXT NOT NULL, qqsec TEXT NOT NULL, govlot TEXT NOT NULL,
		latitude REAL NOT NULL, longitude REAL NOT NULL, data BLOB NOT NULL)`,
		`CREATE VIRTUAL TABLE spatial USING rtree(id, min_lon, max_lon, min_lat, max_lat, +feature_id, +layer)`,
		`CREATE TABLE unlocated (id INTEGER PRIMARY KEY, layer INTEGER NOT NULL CHECK(layer BETWEEN 1 AND 3), plssid TEXT NOT NULL, frstdivid TEXT NOT NULL, secdivid TEXT NOT NULL, survid TEXT NOT NULL, attributes TEXT NOT NULL, reason TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return fail(err)
		}
	}
	doc, _ := json.Marshal(metadata)
	if _, err := db.Exec("INSERT INTO pack_metadata(id,document,complete) VALUES(1,?,0)", string(doc)); err != nil {
		return fail(err)
	}
	w.zip, err = zlib.NewWriterLevel(&w.buffer, zlib.BestSpeed)
	if err != nil {
		return fail(err)
	}
	if err := w.begin(); err != nil {
		return fail(err)
	}
	return w, nil
}

func (w *PackWriter) begin() error {
	var err error
	w.tx, err = w.db.Begin()
	if err != nil {
		return err
	}
	w.feature, err = w.tx.Prepare(`INSERT INTO features(id,layer,plssid,frstdivid,secdivid,survid,stateabbr,prinmercd,twnshpno,twnshpdir,twnshpfrac,rangeno,rangedir,rangefrac,frstdivtyp,frstdivno,secdivtyp,secdivno,qsec,qqsec,govlot,latitude,longitude,data) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	w.bound, err = w.tx.Prepare(`INSERT INTO spatial(id,min_lon,max_lon,min_lat,max_lat,feature_id,layer) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	w.unlocated, err = w.tx.Prepare(`INSERT INTO unlocated(layer,plssid,frstdivid,secdivid,survid,attributes,reason) VALUES(?,?,?,?,?,?,?)`)
	return err
}

func (w *PackWriter) fail(err error) error {
	if w.failed == nil {
		w.failed = err
	}
	return err
}

// Add preserves every attribute and every coordinate, without quantization or
// geometry simplification. Independent features may share cadastral IDs.
// Any failed Add invalidates this writer; Finish will not publish a partial set.
func (w *PackWriter) Add(layer int, attrs map[string]any, geometry Geometry) error {
	if w == nil || w.closed {
		return errors.New("offline pack writer is closed")
	}
	if w.failed != nil {
		return w.failed
	}
	if layer < 1 || layer > 3 || attrs == nil {
		return w.fail(errors.New("pack feature requires layer 1–3 and attributes"))
	}
	// ZZ is the national residual partition. Its original STATEABBR may name
	// any real state even when the PLSSID is absent or malformed.
	if state := attr(attrs, "STATEABBR"); w.metadata.State != "ZZ" && state != "" && state != w.metadata.State {
		return w.fail(fmt.Errorf("feature state %s differs from pack state %s", state, w.metadata.State))
	}
	point, err := geometry.InteriorPoint()
	if err != nil {
		return w.fail(fmt.Errorf("pack polygon: %w", err))
	}
	bounds, err := geometry.IndexBounds()
	if err != nil {
		return w.fail(fmt.Errorf("pack polygon bounds: %w", err))
	}
	raw, err := json.Marshal(packFeature{Attributes: attrs, Geometry: geometry})
	if err != nil {
		return w.fail(fmt.Errorf("encode pack feature: %w", err))
	}
	if len(raw) > maxPackFeatureBytes {
		return w.fail(errors.New("offline pack feature exceeds 64 MiB"))
	}
	w.buffer.Reset()
	w.zip.Reset(&w.buffer)
	if _, err := w.zip.Write(raw); err != nil {
		return w.fail(err)
	}
	if err := w.zip.Close(); err != nil {
		return w.fail(err)
	}
	w.nextID++
	args := []any{w.nextID, layer}
	for _, key := range []string{"PLSSID", "FRSTDIVID", "SECDIVID", "SURVID", "STATEABBR", "PRINMERCD", "TWNSHPNO", "TWNSHPDIR", "TWNSHPFRAC", "RANGENO", "RANGEDIR", "RANGEFRAC", "FRSTDIVTYP", "FRSTDIVNO", "SECDIVTYP", "SECDIVNO", "QSEC", "QQSEC", "GOVLOT"} {
		args = append(args, attr(attrs, key))
	}
	args = append(args, point.Latitude, point.Longitude, w.buffer.Bytes())
	if _, err := w.feature.Exec(args...); err != nil {
		return w.fail(fmt.Errorf("insert pack feature: %w", err))
	}
	for _, b := range bounds {
		w.nextBound++
		// SQLite RTree rounds lower bounds down and upper bounds up in its
		// float32 index. Exact full-float64 rings remain in the feature blob.
		if _, err := w.bound.Exec(w.nextBound, b.MinLon, b.MaxLon, b.MinLat, b.MaxLat, w.nextID, layer); err != nil {
			return w.fail(fmt.Errorf("index pack feature: %w", err))
		}
	}
	w.metadata.Counts[layerName(layer)]++
	return w.advanceBatch()
}

// AddUnlocated preserves an otherwise valid source record known to have absent
// geometry. Callers must not use this to discard a nonempty malformed polygon;
// those records must pass through Add and fail the import if invalid.
func (w *PackWriter) AddUnlocated(layer int, attrs map[string]any) error {
	if w == nil || w.closed {
		return errors.New("offline pack writer is closed")
	}
	if w.failed != nil {
		return w.failed
	}
	if layer < 1 || layer > 3 || attrs == nil {
		return w.fail(errors.New("unlocated pack record requires layer 1–3 and attributes"))
	}
	if state := attr(attrs, "STATEABBR"); w.metadata.State != "ZZ" && state != "" && state != w.metadata.State {
		return w.fail(fmt.Errorf("feature state %s differs from pack state %s", state, w.metadata.State))
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return w.fail(fmt.Errorf("encode unlocated record: %w", err))
	}
	if len(raw) > maxPackFeatureBytes {
		return w.fail(errors.New("unlocated record exceeds 64 MiB"))
	}
	reason := "BLM published this record without polygon geometry"
	if note := attr(attrs, "SECDIVNOTE"); note != "" {
		reason += "; " + note
	}
	if _, err := w.unlocated.Exec(layer, attr(attrs, "PLSSID"), attr(attrs, "FRSTDIVID"), attr(attrs, "SECDIVID"), attr(attrs, "SURVID"), string(raw), reason); err != nil {
		return w.fail(fmt.Errorf("insert unlocated record: %w", err))
	}
	w.metadata.UnlocatedCounts[layerName(layer)]++
	return w.advanceBatch()
}

func (w *PackWriter) advanceBatch() error {
	w.batch++
	if w.batch >= packBatchSize {
		if err := w.commit(); err != nil {
			return w.fail(err)
		}
		if err := w.begin(); err != nil {
			return w.fail(err)
		}
	}
	return nil
}

func (w *PackWriter) commit() error {
	if w.feature != nil {
		_ = w.feature.Close()
		w.feature = nil
	}
	if w.bound != nil {
		_ = w.bound.Close()
		w.bound = nil
	}
	if w.unlocated != nil {
		_ = w.unlocated.Close()
		w.unlocated = nil
	}
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx = nil
	w.batch = 0
	return err
}

// Finish builds description/ID indexes, marks the file complete, syncs it and
// closes all handles. A successful return is required before publishing it.
func (w *PackWriter) Finish() (PackMetadata, error) {
	if w == nil || w.closed {
		return PackMetadata{}, errors.New("offline pack writer is closed")
	}
	if w.failed != nil {
		err := w.failed
		_ = w.Close()
		return PackMetadata{}, err
	}
	finishError := func(err error) (PackMetadata, error) {
		_ = w.Close()
		return PackMetadata{}, fmt.Errorf("finish offline pack: %w", err)
	}
	if err := w.commit(); err != nil {
		return finishError(err)
	}
	// Building B-tree indexes once is substantially cheaper than maintaining
	// them for each feature during a million-feature import.
	for _, statement := range []string{
		`CREATE INDEX features_plssid ON features(layer,plssid)`,
		`CREATE INDEX features_frstdivid ON features(frstdivid) WHERE layer=2`,
		`CREATE INDEX features_secdivid ON features(secdivid) WHERE layer=3`,
		`CREATE INDEX features_survid ON features(survid) WHERE layer=3`,
		`CREATE INDEX features_description ON features(stateabbr,prinmercd,twnshpno,twnshpdir,twnshpfrac,rangeno,rangedir,rangefrac) WHERE layer=1`,
		`CREATE INDEX unlocated_plssid ON unlocated(layer,plssid)`,
		`CREATE INDEX unlocated_frstdivid ON unlocated(frstdivid) WHERE layer=2`,
		`CREATE INDEX unlocated_secdivid ON unlocated(secdivid) WHERE layer=3`,
		`CREATE INDEX unlocated_survid ON unlocated(survid) WHERE layer=3`,
		`ANALYZE`, "PRAGMA journal_mode=DELETE", "PRAGMA synchronous=FULL",
	} {
		if _, err := w.db.Exec(statement); err != nil {
			return finishError(err)
		}
	}
	doc, err := json.Marshal(w.metadata)
	if err != nil {
		return finishError(err)
	}
	if _, err := w.db.Exec("UPDATE pack_metadata SET document=?,complete=1 WHERE id=1", string(doc)); err != nil {
		return finishError(err)
	}
	// The final FULL-synchronous transaction syncs the database. No WAL is
	// used, so the resulting SQLite file is self-contained for atomic rename.
	metadata := cloneMetadata(w.metadata)
	if err := w.Close(); err != nil {
		return PackMetadata{}, err
	}
	return metadata, nil
}

// Close abandons any pending transaction and closes the writer. It never
// removes the file and never turns an incomplete pack into a completed one.
func (w *PackWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	if w.feature != nil {
		_ = w.feature.Close()
	}
	if w.bound != nil {
		_ = w.bound.Close()
	}
	if w.unlocated != nil {
		_ = w.unlocated.Close()
	}
	if w.tx != nil {
		_ = w.tx.Rollback()
		w.tx = nil
	}
	if w.zip != nil {
		_ = w.zip.Close()
	}
	if w.db != nil {
		return w.db.Close()
	}
	return nil
}

// Pack is a completed, read-only offline PLSS provider. It never accesses the
// network. SQLite pages and candidate geometries are loaded on demand.
type Pack struct {
	db           *sql.DB
	metadata     PackMetadata
	hasUnlocated bool
}

// OpenPack opens a completed format-compatible pack without creating files or
// modifying the database. Partial, unrelated, and unsupported files fail.
func OpenPack(path string) (*Pack, error) {
	dsn, err := packDSN(path, "ro")
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	fail := func(err error) (*Pack, error) { _ = db.Close(); return nil, fmt.Errorf("open offline pack: %w", err) }
	var application, version int
	if err := db.QueryRow("PRAGMA application_id").Scan(&application); err != nil {
		return fail(err)
	}
	if application != packApplicationID {
		return fail(errors.New("not a PLSS offline pack"))
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != packVersion {
		return fail(fmt.Errorf("unsupported pack version %d (expected %d)", version, packVersion))
	}
	var document string
	var complete int
	if err := db.QueryRow("SELECT document,complete FROM pack_metadata WHERE id=1").Scan(&document, &complete); err != nil {
		return fail(err)
	}
	if complete != 1 {
		return fail(errors.New("pack is incomplete; finish or repeat its download"))
	}
	var metadata PackMetadata
	if err := json.Unmarshal([]byte(document), &metadata); err != nil {
		return fail(fmt.Errorf("invalid pack metadata: %w", err))
	}
	if metadata.Version != packVersion || !statePattern.MatchString(metadata.State) || metadata.Counts == nil {
		return fail(errors.New("invalid or unsupported pack metadata"))
	}
	if _, err := time.Parse(time.RFC3339, metadata.CreatedAt); err != nil {
		return fail(errors.New("invalid pack creation date"))
	}
	for _, key := range []string{"township", "section", "intersected"} {
		if n, ok := metadata.Counts[key]; !ok || n < 0 {
			return fail(errors.New("invalid pack layer counts"))
		}
	}
	if metadata.UnlocatedCounts == nil {
		metadata.UnlocatedCounts = map[string]int{"township": 0, "section": 0, "intersected": 0}
	}
	totalUnlocated := 0
	for _, key := range []string{"township", "section", "intersected"} {
		n, ok := metadata.UnlocatedCounts[key]
		if !ok || n < 0 {
			return fail(errors.New("invalid pack unlocated counts"))
		}
		totalUnlocated += n
	}
	var unlocatedTables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='unlocated'").Scan(&unlocatedTables); err != nil {
		return fail(err)
	}
	if totalUnlocated > 0 && unlocatedTables == 0 {
		return fail(errors.New("pack declares unlocated records but has no unlocated table"))
	}
	// Prepare all required table/column references without scanning a large
	// state pack on every application startup. Corrupt pages fail when read.
	rows, err := db.Query(`SELECT f.id,f.layer,f.latitude,f.longitude,f.data FROM features f WHERE f.id IN (SELECT feature_id FROM spatial WHERE min_lon<=0 AND max_lon>=0 AND min_lat<=0 AND max_lat>=0) LIMIT 0`)
	if err != nil {
		return fail(err)
	}
	if err := rows.Close(); err != nil {
		return fail(err)
	}
	return &Pack{db: db, metadata: metadata, hasUnlocated: unlocatedTables > 0}, nil
}

func cloneMetadata(m PackMetadata) PackMetadata {
	counts := make(map[string]int, len(m.Counts))
	for k, v := range m.Counts {
		counts[k] = v
	}
	m.Counts = counts
	unlocatedCounts := make(map[string]int, len(m.UnlocatedCounts))
	for k, v := range m.UnlocatedCounts {
		unlocatedCounts[k] = v
	}
	m.UnlocatedCounts = unlocatedCounts
	return m
}

// Metadata returns an independent copy of the validated manifest.
func (p *Pack) Metadata() (PackMetadata, error) {
	if p == nil || p.db == nil {
		return PackMetadata{}, errors.New("offline pack is not open")
	}
	return cloneMetadata(p.metadata), nil
}

func (p *Pack) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

func layerName(layer int) string {
	switch layer {
	case 1:
		return "township"
	case 2:
		return "section"
	case 3:
		return "intersected"
	}
	return ""
}

func (p *Pack) queryPack(ctx context.Context, layer int, where string, args ...any) ([]Match, error) {
	if p == nil || p.db == nil {
		return nil, errors.New("offline pack is not open")
	}
	statement := `SELECT layer,latitude,longitude,data FROM features WHERE layer=` + strconv.Itoa(layer) + ` AND (` + where + `) ORDER BY id`
	rows, err := p.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query offline pack: %w", err)
	}
	defer rows.Close()
	var matches []Match
	for rows.Next() {
		var storedLayer int
		var point Point
		var data []byte
		if err := rows.Scan(&storedLayer, &point.Latitude, &point.Longitude, &data); err != nil {
			return nil, fmt.Errorf("read offline feature: %w", err)
		}
		feature, err := decodePackFeature(data)
		if err != nil {
			return nil, err
		}
		if feature.Attributes == nil || !finite(point.Latitude) || !finite(point.Longitude) || point.Latitude < -90 || point.Latitude > 90 || point.Longitude < -180 || point.Longitude > 180 {
			return nil, errors.New("corrupt offline feature attributes or representative")
		}
		if _, err := feature.Geometry.IndexBounds(); err != nil {
			return nil, fmt.Errorf("corrupt offline geometry: %w", err)
		}
		m := Match{Layer: layerName(storedLayer), Attributes: feature.Attributes, Geometry: feature.Geometry, Representative: point, Approximate: true, Source: p.metadata.Source}
		m.Label = label(m)
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read offline pack: %w", err)
	}
	return matches, nil
}

// Lookup queries the RTree, then tests the original full-precision polygons.
// Every boundary match is retained; holes are excluded. Finest-layer fallback
// is identical to Client.Lookup, without any network fallback.
func (p *Pack) Lookup(ctx context.Context, latitude, longitude float64) ([]Match, error) {
	if !finite(latitude) || !finite(longitude) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return nil, errors.New("latitude must be in [-90,90] and longitude in [-180,180]")
	}
	point := Point{Latitude: latitude, Longitude: longitude}
	for _, layer := range []int{3, 2, 1} {
		candidates, err := p.queryPack(ctx, layer, `id IN (SELECT feature_id FROM spatial WHERE min_lon<=? AND max_lon>=? AND min_lat<=? AND max_lat>=? AND layer=?)`, longitude, longitude, latitude, latitude, layer)
		if err != nil {
			return nil, err
		}
		var matches []Match
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			inside, err := candidate.Geometry.ContainsPoint(point)
			if err != nil {
				return nil, fmt.Errorf("offline polygon: %w", err)
			}
			if inside {
				matches = append(matches, candidate)
			}
		}
		if len(matches) > 0 {
			return matches, nil
		}
	}
	return nil, ErrNotFound
}

// Find resolves the same rectangular description as Client.Find using indexed
// township fields and actual published component polygons.
func (p *Pack) Find(ctx context.Context, d Description) ([]Match, error) {
	d, err := normalize(d)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("offline pack is not open")
	}
	if d.State != p.metadata.State {
		return nil, fmt.Errorf("%w: requested %s, downloaded %s", ErrPackCoverage, d.State, p.metadata.State)
	}
	townships, err := p.queryPack(ctx, 1, `stateabbr=? AND prinmercd=? AND twnshpno=? AND twnshpdir=? AND twnshpfrac=? AND rangeno=? AND rangedir=? AND rangefrac=?`, d.State, d.Meridian, fmt.Sprintf("%03d", d.Township), d.TownshipDirection, d.TownshipFraction, fmt.Sprintf("%03d", d.Range), d.RangeDirection, d.RangeFraction)
	if err != nil {
		return nil, err
	}
	if len(townships) == 0 {
		return nil, ErrNotFound
	}
	if d.Section == 0 {
		return townships, nil
	}
	var placeholders []string
	var args []any
	seen := map[string]bool{}
	for _, township := range townships {
		id := township.Attribute("PLSSID")
		if id == "" {
			return nil, errors.New("offline township is missing PLSSID")
		}
		if !seen[id] {
			placeholders = append(placeholders, "?")
			args = append(args, id)
			seen[id] = true
		}
	}
	where := `plssid IN (` + strings.Join(placeholders, ",") + `) AND frstdivtyp='SN' AND frstdivno IN (?,?,?)`
	args = append(args, strconv.Itoa(d.Section), fmt.Sprintf("%02d", d.Section), fmt.Sprintf("%03d", d.Section))
	layer := 2
	if d.Aliquot != "" {
		layer = 3
		where += ` AND ((secdivtyp='A' AND (secdivno=? OR secdivno LIKE ?))`
		args = append(args, d.Aliquot, "%"+d.Aliquot)
		if len(d.Aliquot) == 2 {
			where += ` OR qsec=?`
			args = append(args, d.Aliquot)
		} else if len(d.Aliquot) == 4 {
			where += ` OR qqsec=?`
			args = append(args, d.Aliquot)
		}
		where += `)`
	}
	if d.Lot != "" {
		layer = 3
		values := []any{d.Lot}
		slots := []string{"?"}
		if n, err := strconv.Atoi(d.Lot); err == nil {
			values = append(values, strconv.Itoa(n), fmt.Sprintf("%02d", n), fmt.Sprintf("%03d", n))
			slots = append(slots, "?", "?", "?")
		}
		in := strings.Join(slots, ",")
		where += ` AND ((secdivtyp='L' AND secdivno IN (` + in + `)) OR govlot IN (` + in + `))`
		args = append(args, values...)
		args = append(args, values...)
	}
	matches, err := p.queryPack(ctx, layer, where, args...)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	for i := range matches {
		for _, township := range townships {
			if matches[i].Attribute("PLSSID") == township.Attribute("PLSSID") {
				for k, v := range township.Attributes {
					if _, exists := matches[i].Attributes[k]; !exists {
						matches[i].Attributes[k] = v
					}
				}
				break
			}
		}
		matches[i].Label = label(matches[i])
	}
	return matches, nil
}

// FindID resolves an exact cadastral identifier without assuming uniqueness.
func (p *Pack) FindID(ctx context.Context, kind, id string) ([]Match, error) {
	fields := map[string]string{"township": "plssid", "section": "frstdivid", "division": "secdivid", "intersected": "secdivid", "survey": "survid"}
	layers := map[string]int{"township": 1, "section": 2, "division": 3, "intersected": 3, "survey": 3}
	kind = strings.ToLower(strings.TrimSpace(kind))
	field, ok := fields[kind]
	if !ok {
		return nil, errors.New("PLSS ID kind must be township, section, division (or intersected), or survey")
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 100 || strings.ContainsAny(id, "\r\n\x00") {
		return nil, errors.New("invalid PLSS identifier")
	}
	matches, err := p.queryPack(ctx, layers[kind], field+"=?", id)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		if p.hasUnlocated {
			var count int
			if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM unlocated WHERE layer="+strconv.Itoa(layers[kind])+" AND "+field+"=?", id).Scan(&count); err != nil {
				return nil, fmt.Errorf("query unlocated record: %w", err)
			}
			if count > 0 {
				return nil, fmt.Errorf("%w: %s %q (%d record(s)); inspect the pack's unlocated records for source details", ErrNoGeometry, kind, id, count)
			}
		}
		return nil, ErrNotFound
	}
	return matches, nil
}

// Unlocated audits up to 10,000 records published without geometry. Every raw
// attribute is preserved, including null values and exact JSON integers. A
// larger set returns an error rather than silently truncating the audit.
func (p *Pack) Unlocated(ctx context.Context) ([]UnlocatedRecord, error) {
	if p == nil || p.db == nil {
		return nil, errors.New("offline pack is not open")
	}
	if !p.hasUnlocated {
		return []UnlocatedRecord{}, nil
	}
	rows, err := p.db.QueryContext(ctx, "SELECT layer,attributes,reason FROM unlocated ORDER BY id LIMIT 10001")
	if err != nil {
		return nil, fmt.Errorf("read unlocated records: %w", err)
	}
	defer rows.Close()
	records := []UnlocatedRecord{}
	for rows.Next() {
		var layer int
		var raw, reason string
		if err := rows.Scan(&layer, &raw, &reason); err != nil {
			return nil, err
		}
		if len(raw) > maxPackFeatureBytes {
			return nil, errors.New("unlocated record exceeds 64 MiB")
		}
		if layer < 1 || layer > 3 {
			return nil, errors.New("corrupt unlocated layer")
		}
		var attrs map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&attrs); err != nil || attrs == nil {
			return nil, errors.New("corrupt unlocated attributes")
		}
		records = append(records, UnlocatedRecord{Layer: layerName(layer), Attributes: attrs, Reason: reason})
		if len(records) > 10000 {
			return nil, errors.New("pack has more than 10000 unlocated records; audit the unlocated SQLite table directly")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
