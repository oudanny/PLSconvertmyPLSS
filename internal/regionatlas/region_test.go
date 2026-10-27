package regionatlas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func fixture(t *testing.T, state string, divisionMin, divisionMax float64) string {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{}
	for _, name := range []string{"keys", "index", "geometry"} {
		paths[name] = filepath.Join(dir, name)
	}
	keys := keyindex.NewBuilder()
	geometry, err := compactgeom.NewBuilder(paths["geometry"], 0)
	if err != nil {
		t.Fatal(err)
	}
	defer geometry.Close()
	var entries []spatialindex.Entry
	for i, b := range [][2]float64{{0, 10}, {0, 6}, {divisionMin, divisionMax}} {
		id := uint32(i + 1)
		layer := uint8(i + 1)
		g := plss.Geometry{Rings: [][][]float64{{{b[0], b[0]}, {b[1], b[0]}, {b[1], b[1]}, {b[0], b[1]}, {b[0], b[0]}}}}
		if err := geometry.Add(id, g); err != nil {
			t.Fatal(err)
		}
		// CA deliberately contains the exact NV-prefixed published division ID.
		// Routing by the source state alone would lose that record.
		s := keyindex.Source{ID: id, Layer: layer, Township: state + "T", First: state + "TS", Second: "NVshared", Survey: "independent-survey"}
		if err := keys.Add(s); err != nil {
			t.Fatal(err)
		}
		bounds, err := g.IndexBounds()
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bounds {
			down := func(x float64) float32 {
				y := float32(x)
				if float64(y) > x {
					y = math.Nextafter32(y, float32(math.Inf(-1)))
				}
				return y
			}
			up := func(x float64) float32 {
				y := float32(x)
				if float64(y) < x {
					y = math.Nextafter32(y, float32(math.Inf(1)))
				}
				return y
			}
			entries = append(entries, spatialindex.Entry{MinLon: down(b.MinLon), MaxLon: up(b.MaxLon), MinLat: down(b.MinLat), MaxLat: up(b.MaxLat), ID: id, Layer: layer})
		}
	}
	if _, err := geometry.Finish(); err != nil {
		t.Fatal(err)
	}
	k, err := os.Create(paths["keys"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.WriteTo(k); err != nil {
		t.Fatal(err)
	}
	k.Close()
	index, err := spatialindex.Build(entries, 4)
	if err != nil {
		t.Fatal(err)
	}
	index, err = index.Quantize(8)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(paths["index"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	source := plss.PackMetadata{Version: 1, State: state, Source: "https://example.invalid/" + state, CreatedAt: "2026-10-27T00:00:00Z", Counts: map[string]int{"township": 1, "section": 1, "intersected": 1}, UnlocatedCounts: map[string]int{"intersected": 1}}
	audit := []plss.UnlocatedRecord{{Layer: "intersected", Attributes: map[string]any{"SECDIVID": state + "unlocated", "OBJECTID": json.Number("9007199254740993"), "nullable": nil}, Reason: "source has no geometry"}}
	path := filepath.Join(dir, state+".atlas")
	if _, err := atlas.Build(path, source, "source-"+state, paths, audit); err != nil {
		t.Fatal(err)
	}
	return path
}

func regionFixture(t *testing.T) (string, Manifest, []ShardInput) {
	t.Helper()
	inputs := []ShardInput{{Path: fixture(t, "NV", 0, 1), State: "NV"}, {Path: fixture(t, "CA", .5, 2), State: "CA"}}
	path := filepath.Join(t.TempDir(), "west.atlas")
	m, err := Build(path, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return path, m, inputs
}

func TestRegionGlobalFallbackOverlapAndExactRouting(t *testing.T) {
	path, _, _ := regionFixture(t)
	for _, buffered := range []bool{false, true} {
		r, err := OpenWithOptions(path, Options{Buffered: buffered, MaxOpenShards: 1})
		if err != nil {
			t.Fatal(err)
		}
		if r.OpenShards() != 0 {
			t.Fatal("opening eagerly initialized states")
		}
		for _, tc := range []struct {
			p      float64
			layer  string
			states []string
		}{{.75, "intersected", []string{"NV", "CA"}}, {1.5, "intersected", []string{"CA"}}, {5, "section", []string{"NV", "CA"}}, {9, "township", []string{"NV", "CA"}}} {
			hits, err := r.Lookup(context.Background(), tc.p, tc.p)
			if err != nil {
				t.Fatal(err)
			}
			var states []string
			for _, h := range hits {
				if h.Layer != tc.layer {
					t.Fatalf("per-state fallback leaked: %+v", hits)
				}
				states = append(states, h.State)
			}
			if !reflect.DeepEqual(states, tc.states) {
				t.Fatalf("states %v, want %v", states, tc.states)
			}
			if r.OpenShards() > 1 {
				t.Fatal("cache exceeded explicit cap")
			}
		}
		hits, err := r.FindID("division", "NVshared")
		if err != nil || len(hits) != 2 || hits[0].State != "NV" || hits[1].State != "CA" {
			t.Fatalf("foreign prefix / duplicate lost: %+v %v", hits, err)
		}
		surveys, err := r.FindID("survey", "independent-survey")
		if err != nil || len(surveys) != 2 {
			t.Fatalf("non-state routing failed: %+v %v", surveys, err)
		}
		if _, err := r.FindID("division", "NVunlocated"); !errors.Is(err, plss.ErrNoGeometry) {
			t.Fatalf("audit route: %v", err)
		}
		if _, err := r.FindID("division", "AZmissing"); !errors.Is(err, plss.ErrNotFound) {
			t.Fatalf("missing ID: %v", err)
		}
		if _, err := r.Lookup(context.Background(), 30, 30); !errors.Is(err, plss.ErrNotFound) {
			t.Fatalf("no coverage: %v", err)
		}
		full, err := r.Full(hits[0])
		if err != nil {
			t.Fatal(err)
		}
		point, err := r.Representative(hits[0])
		if err != nil || point != full.Representative {
			t.Fatalf("representative differs: %+v %v", point, err)
		}
		if has, err := r.HasFullAttributes(hits[0]); err != nil || has {
			t.Fatal("core claimed full metadata")
		}
		if err := r.Verify(); err != nil {
			t.Fatal(err)
		}
		r.Close()
		if full.Geometry.Rings[0][1][0] != 1 || full.Attributes["SECDIVID"] != "NVshared" {
			t.Fatal("retained result changed after close")
		}
	}
}

func TestRegionAuditProvenanceAndStandalone(t *testing.T) {
	path, m, inputs := regionFixture(t)
	r, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	audit, err := r.Unlocated(context.Background())
	if err != nil || len(audit) != 2 {
		t.Fatalf("audit: %+v %v", audit, err)
	}
	if audit[0].Attributes["OBJECTID"] != json.Number("9007199254740993") {
		t.Fatal("audit lost integer precision")
	}
	audit[0].Attributes["SECDIVID"] = "mutated"
	if _, err := r.FindID("division", "NVunlocated"); !errors.Is(err, plss.ErrNoGeometry) {
		t.Fatal("caller mutated cached audit")
	}
	sources := r.Sources()
	sources[0].Counts["township"] = 999
	if r.Sources()[0].Counts["township"] != 1 {
		t.Fatal("caller mutated source counts")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for i, s := range m.Shards {
		original, err := os.ReadFile(inputs[i].Path)
		if err != nil {
			t.Fatal(err)
		}
		copied := make([]byte, s.Section.Size)
		if _, err := file.ReadAt(copied, s.Section.Offset); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, copied) {
			t.Fatal("state bytes changed in bundle")
		}
	}
	single, err := OpenMapped(inputs[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	hits, err := single.Lookup(context.Background(), .5, .5)
	if err != nil || len(hits) != 1 || hits[0].State != "NV" {
		t.Fatalf("standalone: %+v %v", hits, err)
	}
	if err := single.Verify(); err != nil {
		t.Fatal(err)
	}
}

func mutateManifest(t *testing.T, original []byte, change func(*Manifest)) []byte {
	t.Helper()
	data := append([]byte(nil), original...)
	n := int(binary.LittleEndian.Uint32(data[8:12]))
	var m Manifest
	if err := json.Unmarshal(data[44:44+n], &m); err != nil {
		t.Fatal(err)
	}
	change(&m)
	doc, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(data[8:12], uint32(len(doc)))
	copy(data[44:], doc)
	h := sha256.Sum256(doc)
	copy(data[12:44], h[:])
	return data
}

func TestRegionRejectsCorruptionMismatchAndOverwrite(t *testing.T) {
	path, _, inputs := regionFixture(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(path, inputs); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite: %v", err)
	}
	for name, data := range map[string][]byte{
		"truncated":       original[:len(original)-1],
		"wrong-offset":    mutateManifest(t, original, func(m *Manifest) { m.Shards[0].Section.Offset++ }),
		"wrong-source":    mutateManifest(t, original, func(m *Manifest) { m.Shards[0].Source.CreatedAt = "changed" }),
		"wrong-extent":    mutateManifest(t, original, func(m *Manifest) { m.Shards[0].Bounds.MaxLon = 9 }),
		"duplicate-state": mutateManifest(t, original, func(m *Manifest) { m.Shards[1].State = "NV" }),
		"bad-routes":      mutateManifest(t, original, func(m *Manifest) { m.Shards[0].Prefixes = []string{"NV", "CA"} }),
		"missing-route":   mutateManifest(t, original, func(m *Manifest) { m.Shards[0].Prefixes = []string{"NV"} }),
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "bad.atlas")
			os.WriteFile(p, data, 0600)
			r, err := OpenMapped(p)
			if err == nil {
				r.Close()
				t.Fatal("corrupt bundle opened")
			}
		})
	}
	bad := append([]byte(nil), original...)
	bad[45] ^= 1
	p := filepath.Join(t.TempDir(), "header.atlas")
	os.WriteFile(p, bad, 0600)
	if r, err := OpenMapped(p); err == nil {
		r.Close()
		t.Fatal("header digest ignored")
	}
	for _, inputs := range [][]ShardInput{{inputs[0], inputs[0]}, {{Path: inputs[0].Path, State: "CA"}}} {
		out := filepath.Join(t.TempDir(), "failed.atlas")
		if _, err := Build(out, inputs); err == nil {
			t.Fatal("mismatched shards accepted")
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed build left a published file")
		}
	}
	for _, field := range []string{"spatial", "unlocated"} {
		t.Run("source-count-"+field, func(t *testing.T) {
			data, err := os.ReadFile(inputs[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := atlas.ReadManifest(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if field == "spatial" {
				manifest.Source.Counts["section"] = 0
				manifest.Source.Counts["township"] = 2
			} else {
				manifest.Source.UnlocatedCounts["intersected"] = 0
			}
			doc, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			binary.LittleEndian.PutUint32(data[8:12], uint32(len(doc)))
			copy(data[12:], doc)
			p := filepath.Join(t.TempDir(), "mismatched.atlas")
			if err := os.WriteFile(p, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Build(filepath.Join(t.TempDir(), "region.atlas"), []ShardInput{{Path: p}}); err == nil {
				t.Fatal("source layer counts were trusted")
			}
		})
	}
}

func TestRegionConcurrencyCloseAndCanceledCacheWait(t *testing.T) {
	path, _, _ := regionFixture(t)
	r, err := OpenWithOptions(path, Options{MaxOpenShards: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Pin one shard, then verify a waiter can cancel without loading a second.
	r.mu.RLock()
	_, done, err := r.acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, _, err = r.acquire(ctx, 1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cache waiter: %v", err)
	}
	done()
	r.mu.RUnlock()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	ready := make(chan struct{}, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Lookup(context.Background(), .75, .75); err != nil {
				errs <- err
			}
			ready <- struct{}{}
			<-start
			for j := 0; j < 30; j++ {
				_, err := r.Lookup(context.Background(), .75, .75)
				if err != nil && !errors.Is(err, os.ErrClosed) {
					errs <- err
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-ready
	}
	close(start)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FindID("division", "NVshared"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("query after close: %v", err)
	}
	if _, err := r.Full(Hit{State: "NV", Hit: atlas.Hit{FeatureID: 3}}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("full after close: %v", err)
	}
	if err := r.Verify(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("verify after close: %v", err)
	}
}

type observedContext struct {
	context.Context
	check func() error
}

func (c observedContext) Err() error { return c.check() }

func TestBuildContextCancellationAndAtomicNoOverwrite(t *testing.T) {
	input := fixture(t, "NV", 0, 1)
	for _, phase := range []string{"already-canceled", "copying", "publish-race"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "region.atlas")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "already-canceled" {
				cancel()
			}
			observed := false
			checked := observedContext{Context: ctx, check: func() error {
				if phase != "already-canceled" {
					files, err := filepath.Glob(filepath.Join(dir, ".plss-region-*.partial"))
					if err != nil {
						return err
					}
					for _, file := range files {
						info, err := os.Stat(file)
						if err != nil {
							return err
						}
						if phase == "copying" && info.Size() > headerSize {
							if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
								return errors.New("partial output was published")
							}
							observed = true
							cancel()
						}
						if phase == "publish-race" && !observed {
							f, err := os.Open(file)
							if err != nil {
								return err
							}
							var h [8]byte
							_, err = f.Read(h[:])
							f.Close()
							if err != nil {
								return err
							}
							if string(h[:]) == magic {
								observed = true
								if err := os.WriteFile(out, []byte("existing destination"), 0600); err != nil {
									return err
								}
							}
						}
					}
				}
				return ctx.Err()
			}}
			_, err := BuildContext(checked, out, []ShardInput{{Path: input}})
			if phase == "publish-race" {
				if !observed || !errors.Is(err, os.ErrExist) {
					t.Fatalf("publication race: observed=%t, %v", observed, err)
				}
				data, e := os.ReadFile(out)
				if e != nil || string(data) != "existing destination" {
					t.Fatal("racing destination was replaced")
				}
			} else {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
				if phase == "copying" && !observed {
					t.Fatal("copy cancellation was not reached")
				}
				if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("canceled build published output")
				}
			}
			files, err := filepath.Glob(filepath.Join(dir, ".plss-region-*.partial"))
			if err != nil || len(files) != 0 {
				t.Fatalf("staging cleanup: %v %v", files, err)
			}
		})
	}
}
