package atlas_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func TestBorrowedReadersRespectSectionAndOwnerLifetime(t *testing.T) {
	f := buildFixture(t, false, 0, 8)
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	// Neither borrowed opener may assume its source begins at file offset0.
	outer := append([]byte("unrelated prefix"), data...)
	outer = append(outer, []byte("unrelated suffix")...)
	section := io.NewSectionReader(bytes.NewReader(outer), int64(len("unrelated prefix")), int64(len(data)))
	view, err := atlas.OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	buffered, err := atlas.OpenReaderAt(section, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*atlas.Reader{view, buffered} {
		if err := r.Verify(); err != nil {
			t.Fatal(err)
		}
		got, err := r.Lookup(context.Background(), .5, 1)
		if err != nil {
			t.Fatal(err)
		}
		want, err := f.reader.Lookup(context.Background(), .5, 1)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("borrowed result: %+v %v", got, err)
		}
		manifest, err := atlas.ReadManifest(section, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		bounds, has, err := atlas.ReadBounds(section, manifest)
		if err != nil {
			t.Fatal(err)
		}
		ownedBounds, ownedHas, err := r.Bounds()
		if err != nil || has != ownedHas || bounds != ownedBounds {
			t.Fatalf("independent bounds disagree: %+v %+v %v", bounds, ownedBounds, err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		// Closing a view does not change or close the enclosing source.
		var magic [8]byte
		if _, err := section.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "PLSSATL1" {
			t.Fatal("borrowed owner was closed")
		}
	}
}

func TestUnlocatedReferencesDoNotInventParentLayerRecords(t *testing.T) {
	f := buildFixture(t, false, 0, 8)
	f.audit[0].Attributes["PLSSID"] = "orphan-town"
	f.audit[0].Attributes["FRSTDIVID"] = "orphan-section"
	f.audit[0].Attributes["SURVID"] = "orphan-survey"
	path := filepath.Join(t.TempDir(), "audit.atlas")
	if _, err := atlas.Build(path, f.source, "fixture", f.paths, f.audit); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(string) (*atlas.Reader, error){atlas.Open, atlas.OpenMapped} {
		r, err := open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			kind, id string
			want     error
		}{{"township", "orphan-town", plss.ErrNotFound}, {"section", "orphan-section", plss.ErrNotFound}, {"division", "unlocated-id", plss.ErrNoGeometry}, {"survey", "orphan-survey", plss.ErrNoGeometry}} {
			if _, err := r.FindID(tc.kind, tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("%s/%s: %v, want %v", tc.kind, tc.id, err, tc.want)
			}
		}
		r.Close()
	}
}
