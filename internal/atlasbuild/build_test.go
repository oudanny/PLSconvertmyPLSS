package atlasbuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.sqlite")
	w, err := plss.CreatePack(path, plss.PackMetadata{State: "WA"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	g := plss.Geometry{Rings: [][][]float64{{{-122, 47, 5}, {-121, 47, 6}, {-121, 48, 7}, {-122, 48, 8}, {-122, 47, 5}}}}
	for layer := 1; layer <= 3; layer++ {
		attrs := map[string]any{"STATEABBR": "WA", "PLSSID": "WA330010N0010E0"}
		if layer >= 2 {
			attrs["FRSTDIVID"] = "WA330010N0010E0SN010"
		}
		if layer == 3 {
			attrs["SECDIVID"] = "WA330010N0010E0SN010ANENE"
			attrs["SURVID"] = "special-survey"
		}
		if err = w.Add(layer, attrs, g); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.AddUnlocated(3, map[string]any{"STATEABBR": "WA", "SECDIVID": "WA-unlocated"}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Finish(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildExactVerifiedAndNoOverwrite(t *testing.T) {
	source := fixture(t)
	for _, codec := range []string{"raw", "compressed", "packed"} {
		t.Run(codec, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "complete.atlas")
			r, err := Build(context.Background(), source, out, Options{CompressedVertices: codec == "compressed", PackedVertices: codec == "packed"})
			if err != nil {
				t.Fatal(err)
			}
			if r.Records != 3 || r.GeometryVerified != 3 || r.IdentifiersVerified != 3 || r.Unlocated != 1 || len(r.SourceSHA256) != 64 || len(r.GeometrySHA256) != 64 {
				t.Fatalf("bad report: %+v", r)
			}
			a, err := atlas.OpenMapped(out)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			hits, err := a.Lookup(context.Background(), 47.5, -121.5)
			if err != nil || len(hits) != 1 || hits[0].ID != "WA330010N0010E0SN010ANENE" {
				t.Fatalf("%v %v", hits, err)
			}
			m, err := a.Full(hits[0])
			if err != nil || m.Geometry.Rings[0][2][2] != 7 {
				t.Fatalf("extra ordinate changed: %v", err)
			}
			if _, err = a.FindID("division", "WA-unlocated"); !errors.Is(err, plss.ErrNoGeometry) {
				t.Fatal(err)
			}
			if _, err = Build(context.Background(), source, out, Options{}); err == nil {
				t.Fatal("overwrote existing file")
			}
		})
	}
}

func TestBuildCanceledDoesNotPublish(t *testing.T) {
	source := fixture(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.atlas")
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Build(ctx, source, out, Options{Progress: func(string, uint32, uint32) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err = os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published canceled build")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files remain: %v %v", files, err)
	}
}

func TestSourceHashRejectsJournalState(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			path := fixture(t)
			if err := os.WriteFile(path+suffix, []byte("pending"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := SourceHash(context.Background(), path); err == nil {
				t.Fatal("ignored source sidecar")
			}
		})
	}
	path := fixture(t)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte{2, 2}, 18); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := SourceHash(context.Background(), path); err == nil {
		t.Fatal("accepted WAL-mode main file")
	}
}
