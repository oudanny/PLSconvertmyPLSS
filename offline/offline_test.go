package offline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/offline"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func stateAtlas(t *testing.T, state string) string {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{}
	for _, name := range []string{"keys", "index", "geometry"} {
		paths[name] = filepath.Join(dir, name)
	}
	k := keyindex.NewBuilder()
	if err := k.Add(keyindex.Source{ID: 1, Layer: 3, Township: state + "T", First: state + "S", Second: state + "D"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(paths["keys"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	g, err := compactgeom.NewBuilder(paths["geometry"], 0)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := g.Add(1, plss.Geometry{Rings: [][][]float64{{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Finish(); err != nil {
		t.Fatal(err)
	}
	index, err := spatialindex.Build([]spatialindex.Entry{{MinLon: 0, MaxLon: 1, MinLat: 0, MaxLat: 1, ID: 1, Layer: 3}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	f, err = os.Create(paths["index"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	path := filepath.Join(dir, state+".atlas")
	if _, err := atlas.Build(path, plss.PackMetadata{State: state, Source: "fixture", Counts: map[string]int{"intersected": 1}}, "source-hash", paths, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// This external-package integration test exercises only exported offline types
// after constructing a synthetic bundle. Consumer code needs no internal API.
func TestPublicReaderPreservesCrossStateRecordsAndOwnership(t *testing.T) {
	inputs := []regionatlas.ShardInput{{Path: stateAtlas(t, "NV")}, {Path: stateAtlas(t, "CA")}}
	path := filepath.Join(t.TempDir(), "west.atlas")
	if _, err := regionatlas.Build(path, inputs); err != nil {
		t.Fatal(err)
	}
	for _, options := range []offline.Options{{}, {Buffered: true, LazyStateCache: 1}} {
		r, err := offline.OpenWithOptions(path, options)
		if err != nil {
			t.Fatal(err)
		}
		hits, err := r.Lookup(context.Background(), .5, .5)
		if err != nil || len(hits) != 2 {
			t.Fatalf("lookup: %+v %v", hits, err)
		}
		if hits[0].State != "NV" || hits[1].State != "CA" || hits[0].FeatureID != 1 || hits[0].Layer != "intersected" {
			t.Fatalf("public identities: %+v", hits)
		}
		exact, err := r.FindID("division", "CAD")
		if err != nil || len(exact) != 1 || exact[0] != hits[1] {
			t.Fatalf("exact ID: %+v %v", exact, err)
		}
		p, err := r.Representative(hits[0])
		if err != nil {
			t.Fatal(err)
		}
		full, err := r.Full(hits[0])
		if err != nil || full.Representative != p {
			t.Fatalf("full: %+v %v", full, err)
		}
		if has, err := r.HasFullAttributes(hits[0]); err != nil || has {
			t.Fatal("core attribute status")
		}
		sources := r.Sources()
		if len(sources) != 2 {
			t.Fatal("source provenance")
		}
		sources[0].Counts["intersected"] = 7
		if r.Sources()[0].Counts["intersected"] != 1 {
			t.Fatal("sources alias internal metadata")
		}
		if err := r.Verify(); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		if full.Geometry.Rings[0][1][0] != 1 || full.Attributes["SECDIVID"] != "NVD" {
			t.Fatal("returned full record lost ownership")
		}
		if _, err := r.Lookup(context.Background(), .5, .5); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("closed reader: %v", err)
		}
	}
}
