package plss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DownloadOptions configures a full, lossless state data pack. Nevada is the
// initial supported state. HTTPClient may be supplied for proxies or testing.
// Progress runs synchronously on the importing goroutine, never concurrently.
type DownloadOptions struct {
	State      string
	BaseURL    string
	HTTPClient *http.Client
	Workers    int
	Progress   func(DownloadProgress)
}

type DownloadProgress struct {
	Layer     int
	Completed int
	Total     int
}

const downloadBatchSize = 1000
const downloadResponseLimit = 128 * 1024 * 1024

type packDownloader struct {
	base     string
	http     *http.Client
	workers  int
	progress func(DownloadProgress)
}

type layerSnapshot struct {
	layer int
	field string
	ids   []int64
}

type downloadFeature struct {
	Attributes map[string]any `json:"attributes"`
	Geometry   Geometry       `json:"geometry"`
}

// DownloadPack downloads every record whose PLSSID begins with the selected
// state from BLM township, section, and intersected layers. It snapshots each
// layer's object IDs before downloading and verifies every expected feature.
// Records without source geometry are confirmed in the native spatial reference
// and retained as unlocated audit records; they are not spatially indexed.
// A changing upstream dataset can cause an explicit error; a partial pack is
// never published. This ID snapshot is not a transactional BLM history snapshot.
//
// The destination must not exist. A temporary sibling directory is removed on
// success, cancellation, or failure. Publishing uses a same-filesystem hard
// link, which atomically refuses an existing destination even on Unix (where
// os.Rename would replace it). The filesystem must support hard links.
func DownloadPack(ctx context.Context, path string, options DownloadOptions) (PackMetadata, error) {
	var zero PackMetadata
	state := strings.ToUpper(strings.TrimSpace(options.State))
	if state == "" {
		state = "NV"
	}
	if state != "NV" {
		return zero, errors.New("offline pack downloads currently support Nevada (NV) only")
	}
	if strings.TrimSpace(path) == "" {
		return zero, errors.New("a data pack output path is required")
	}
	workers := options.Workers
	if workers == 0 {
		workers = 4
	}
	if workers < 1 || workers > 8 {
		return zero, errors.New("download workers must be between 1 and 8")
	}
	base := strings.TrimRight(options.BaseURL, "/")
	if base == "" {
		base = DefaultURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return zero, errors.New("download service URL must be an absolute http(s) MapServer URL without query or fragment")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return zero, err
	}
	if _, err := os.Lstat(path); err == nil {
		return zero, fmt.Errorf("data pack output already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	d := packDownloader{base: base, http: client, workers: workers, progress: options.Progress}
	snapshots := make([]layerSnapshot, 0, 3)
	for _, layer := range []int{1, 2, 3} {
		snapshot, err := d.snapshot(ctx, layer, state)
		if err != nil {
			return zero, err
		}
		snapshots = append(snapshots, snapshot)
		if d.progress != nil {
			d.progress(DownloadProgress{Layer: layer, Total: len(snapshot.ids)})
		}
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return zero, fmt.Errorf("create output directory: %w", err)
	}
	tempDir, err := os.MkdirTemp(parent, ".plss-download-")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(tempDir)
	tempPath := filepath.Join(tempDir, "pack.sqlite")
	metadata := PackMetadata{Version: 1, State: state, Source: base, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	writer, err := CreatePack(tempPath, metadata)
	if err != nil {
		return zero, err
	}
	defer writer.Close()
	for _, snapshot := range snapshots {
		if err := d.importLayer(ctx, writer, snapshot); err != nil {
			return zero, err
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	metadata, err = writer.Finish()
	if err != nil {
		return zero, fmt.Errorf("finish data pack: %w", err)
	}
	for _, snapshot := range snapshots {
		name := map[int]string{1: "township", 2: "section", 3: "intersected"}[snapshot.layer]
		stored := metadata.Counts[name] + metadata.UnlocatedCounts[name]
		if stored != len(snapshot.ids) {
			return zero, fmt.Errorf("data pack layer %d count mismatch: wrote %d located and %d unlocated, expected %d", snapshot.layer, metadata.Counts[name], metadata.UnlocatedCounts[name], len(snapshot.ids))
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := os.Link(tempPath, path); err != nil {
		return zero, fmt.Errorf("publish data pack without overwriting (filesystem must support hard links): %w", err)
	}
	return metadata, nil
}

func (d *packDownloader) snapshot(ctx context.Context, layer int, state string) (layerSnapshot, error) {
	params := url.Values{"f": {"json"}, "where": {"PLSSID LIKE '" + state + "%'"}, "returnIdsOnly": {"true"}}
	body, err := d.post(ctx, layer, params)
	if err != nil {
		return layerSnapshot{}, err
	}
	var result struct {
		ObjectIDFieldName     string   `json:"objectIdFieldName"`
		ObjectIDs             *[]int64 `json:"objectIds"`
		ExceededTransferLimit bool     `json:"exceededTransferLimit"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return layerSnapshot{}, fmt.Errorf("decode layer %d object IDs: %w", layer, err)
	}
	if result.ObjectIDFieldName == "" || result.ObjectIDs == nil || len(*result.ObjectIDs) == 0 {
		return layerSnapshot{}, fmt.Errorf("layer %d has no object-ID snapshot for %s", layer, state)
	}
	if result.ExceededTransferLimit {
		return layerSnapshot{}, fmt.Errorf("layer %d object-ID snapshot was truncated", layer)
	}
	ids := *result.ObjectIDs
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if id <= 0 {
			return layerSnapshot{}, fmt.Errorf("invalid object ID %d in layer %d", id, layer)
		}
		if len(unique) == 0 || id != unique[len(unique)-1] {
			unique = append(unique, id)
		}
	}
	return layerSnapshot{layer: layer, field: result.ObjectIDFieldName, ids: unique}, nil
}

func (d *packDownloader) importLayer(ctx context.Context, writer *PackWriter, snapshot layerSnapshot) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type batchResult struct {
		features []downloadFeature
		err      error
	}
	jobs := make(chan []int64)
	results := make(chan batchResult, d.workers)
	var workers sync.WaitGroup
	for i := 0; i < d.workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ids := range jobs {
				features, err := d.batch(workCtx, snapshot.layer, snapshot.field, ids)
				select {
				case results <- batchResult{features: features, err: err}:
				case <-workCtx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := 0; i < len(snapshot.ids); i += downloadBatchSize {
			end := i + downloadBatchSize
			if end > len(snapshot.ids) {
				end = len(snapshot.ids)
			}
			select {
			case jobs <- snapshot.ids[i:end]:
			case <-workCtx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	completed := 0
	var firstErr error
	for result := range results {
		if firstErr != nil {
			continue
		}
		if result.err != nil {
			firstErr = result.err
			cancel()
			continue
		}
		for _, feature := range result.features {
			if err := ctx.Err(); err != nil {
				firstErr = err
				cancel()
				break
			}
			var err error
			if len(feature.Geometry.Rings) == 0 {
				// batch already confirmed this is genuinely absent source shape,
				// rather than an omitted or failed projected geometry response.
				err = writer.AddUnlocated(snapshot.layer, feature.Attributes)
			} else {
				err = writer.Add(snapshot.layer, feature.Attributes, feature.Geometry)
			}
			if err != nil {
				firstErr = fmt.Errorf("write layer %d: %w", snapshot.layer, err)
				cancel()
				break
			}
			completed++
		}
		if firstErr == nil && d.progress != nil {
			d.progress(DownloadProgress{Layer: snapshot.layer, Completed: completed, Total: len(snapshot.ids)})
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if completed != len(snapshot.ids) {
		return fmt.Errorf("incomplete layer %d: imported %d of %d polygons", snapshot.layer, completed, len(snapshot.ids))
	}
	return nil
}

func (d *packDownloader) batch(ctx context.Context, layer int, field string, ids []int64) ([]downloadFeature, error) {
	values := make([]string, len(ids))
	expected := make(map[int64]bool, len(ids))
	for i, id := range ids {
		values[i] = strconv.FormatInt(id, 10)
		expected[id] = false
	}
	params := url.Values{"f": {"json"}, "objectIds": {strings.Join(values, ",")}, "outFields": {"*"}, "returnGeometry": {"true"}, "outSR": {"4326"}}
	body, err := d.post(ctx, layer, params)
	if err != nil {
		return nil, err
	}
	var result struct {
		Features              *[]downloadFeature `json:"features"`
		ExceededTransferLimit bool               `json:"exceededTransferLimit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode layer %d features: %w", layer, err)
	}
	if result.Features == nil {
		return nil, fmt.Errorf("layer %d response has no features", layer)
	}
	if result.ExceededTransferLimit {
		return nil, fmt.Errorf("layer %d feature batch was truncated; no partial pack was published", layer)
	}
	for _, feature := range *result.Features {
		value, ok := feature.Attributes[field].(json.Number)
		if !ok {
			return nil, fmt.Errorf("layer %d feature is missing numeric %s", layer, field)
		}
		id, err := value.Int64()
		if err != nil {
			return nil, fmt.Errorf("layer %d feature has an invalid object ID", layer)
		}
		seen, wanted := expected[id]
		if !wanted {
			return nil, fmt.Errorf("layer %d returned unexpected object ID %d", layer, id)
		}
		if seen {
			return nil, fmt.Errorf("layer %d returned duplicate object ID %d", layer, id)
		}
		expected[id] = true
		if len(feature.Geometry.Rings) == 0 {
			if err := d.confirmUnlocated(ctx, layer, field, id); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range ids {
		if !expected[id] {
			return nil, fmt.Errorf("layer %d omitted expected object ID %d; source may have changed during download", layer, id)
		}
	}
	return *result.Features, nil
}

// confirmUnlocated re-queries a single missing geometry in the service's native
// reference (no outSR) to distinguish a genuinely absent source polygon from a
// reprojection failure. Source shape area must explicitly be null or zero.
// GISACRE is a descriptive attribute and may be stale; it is not authoritative
// evidence of a stored shape. All original batch attributes remain preserved.
func (d *packDownloader) confirmUnlocated(ctx context.Context, layer int, field string, id int64) error {
	params := url.Values{"f": {"json"}, "objectIds": {strconv.FormatInt(id, 10)}, "outFields": {"*"}, "returnGeometry": {"true"}}
	body, err := d.post(ctx, layer, params)
	if err != nil {
		return err
	}
	var result struct {
		Features []struct {
			Attributes map[string]any  `json:"attributes"`
			Geometry   json.RawMessage `json:"geometry"`
		} `json:"features"`
		ExceededTransferLimit bool `json:"exceededTransferLimit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode layer %d native geometry confirmation: %w", layer, err)
	}
	if result.ExceededTransferLimit || len(result.Features) != 1 {
		return fmt.Errorf("layer %d object ID %d missing geometry could not be confirmed from the native source", layer, id)
	}
	feature := result.Features[0]
	got, ok := feature.Attributes[field].(json.Number)
	gotID, parseErr := got.Int64()
	if !ok || parseErr != nil || gotID != id {
		return fmt.Errorf("layer %d native confirmation returned a different object ID than %d", layer, id)
	}
	geometry := bytes.TrimSpace(feature.Geometry)
	if len(geometry) != 0 && !bytes.Equal(geometry, []byte("null")) {
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(geometry, &shape); err != nil {
			return fmt.Errorf("layer %d object ID %d has malformed native geometry", layer, id)
		}
		for key, value := range shape {
			if key == "spatialReference" {
				continue
			}
			if key == "rings" {
				var rings []json.RawMessage
				if err := json.Unmarshal(value, &rings); err == nil && len(rings) == 0 {
					continue
				}
			}
			return fmt.Errorf("layer %d object ID %d has native geometry but its WGS84 geometry was missing; refusing an incomplete spatial import", layer, id)
		}
	}
	areaFound := false
	for key, value := range feature.Attributes {
		if !strings.EqualFold(key, "SHAPE.STArea()") {
			continue
		}
		areaFound = true
		if value == nil {
			continue
		}
		area, ok := value.(json.Number)
		number, parseErr := area.Float64()
		if !ok || parseErr != nil || number != 0 {
			return fmt.Errorf("layer %d object ID %d has missing native geometry but nonzero or invalid source shape area", layer, id)
		}
	}
	if !areaFound {
		return fmt.Errorf("layer %d object ID %d has missing native geometry without an explicit empty source shape area", layer, id)
	}
	return nil
}

// post retries only transient transport, HTTP and ArcGIS failures. Each attempt
// has its own timeout, so a large download has no hidden 30-second total limit.
func (d *packDownloader) post(ctx context.Context, layer int, params url.Values) ([]byte, error) {
	endpoint := d.base + "/" + strconv.Itoa(layer) + "/query"
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		requestCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "PLSconvertmyPLSS/1.0 (Go; BLM offline data pack downloader)")
		resp, requestErr := d.http.Do(req)
		retry := false
		delay := time.Duration(1<<attempt) * 500 * time.Millisecond
		var body []byte
		if requestErr != nil {
			lastErr = fmt.Errorf("layer %d download request: %w", layer, requestErr)
			retry = true
		} else {
			body, err = io.ReadAll(io.LimitReader(resp.Body, downloadResponseLimit+1))
			resp.Body.Close()
			if wait := retryAfter(resp.Header.Get("Retry-After")); wait > delay {
				delay = wait
			}
			if err != nil {
				lastErr = fmt.Errorf("read layer %d: %w", layer, err)
				retry = true
			} else if len(body) > downloadResponseLimit {
				cancel()
				return nil, fmt.Errorf("layer %d response exceeds 128 MiB", layer)
			} else if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("layer %d service returned HTTP %d", layer, resp.StatusCode)
				retry = resp.StatusCode == 429 || resp.StatusCode >= 500
			} else {
				var envelope struct {
					Error *struct {
						Code    int      `json:"code"`
						Message string   `json:"message"`
						Details []string `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil {
					cancel()
					return nil, fmt.Errorf("decode layer %d response: %w", layer, err)
				}
				if envelope.Error == nil {
					cancel()
					return body, nil
				}
				lastErr = fmt.Errorf("layer %d ArcGIS error %d: %s %s", layer, envelope.Error.Code, envelope.Error.Message, strings.Join(envelope.Error.Details, "; "))
				retry = envelope.Error.Code == 429 || envelope.Error.Code >= 500
			}
		}
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !retry || attempt == 4 {
			return nil, lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func retryAfter(value string) time.Duration {
	var delay time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds >= 30 {
			return 30 * time.Second
		}
		if seconds <= 0 {
			return 0
		}
		delay = time.Duration(seconds) * time.Second
	} else if deadline, err := http.ParseTime(value); err == nil {
		delay = time.Until(deadline)
	}
	if delay < 0 {
		return 0
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}
