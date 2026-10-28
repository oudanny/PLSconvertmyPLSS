package regionatlas

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func TestIDPrefilterCannotMaskContradictoryLayerCounts(t *testing.T) {
	path, directory, _ := regionFixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shard := directory.Shards[0]
	m, err := atlas.ReadManifest(bytes.NewReader(data[shard.Section.Offset:shard.Section.Offset+shard.Section.Size]), shard.Section.Size)
	if err != nil {
		t.Fatal(err)
	}
	offset := shard.Section.Offset + m.Sections["keys"].Offset
	n := int64(binary.LittleEndian.Uint32(data[offset+8:]))
	original := data[offset+12 : offset+12+n]
	changed := bytes.Replace(original, []byte(`"layers":[1,1,1]`), []byte(`"layers":[1,2,0]`), 1)
	if bytes.Equal(original, changed) {
		t.Fatal("fixture layout changed")
	}
	copy(original, changed)
	if _, err := OpenReaderAt(bytes.NewReader(data), int64(len(data)), Options{}); err == nil {
		t.Fatal("corrupt key counts can hide a division match")
	}
}

func TestIDAuditShortcutRequiresMatchingMetadata(t *testing.T) {
	path := fixture(t, "NV", 0, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := []byte(`"unlocated_counts":{"intersected":1}`)
	after := []byte(`"unlocated_counts":{"intersected":0}`)
	changed := bytes.Replace(data, before, after, 1)
	if bytes.Equal(data, changed) {
		t.Fatal("fixture audit layout changed")
	}
	if _, err := OpenReaderAt(bytes.NewReader(changed), int64(len(changed)), Options{}); err == nil {
		t.Fatal("audit shortcut accepted inconsistent source counts")
	}
}

type countedSource struct {
	source io.ReaderAt
	read   int64
	closed bool
}

func (s *countedSource) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.source.ReadAt(p, off)
	s.read += int64(n)
	return n, err
}
func (s *countedSource) Close() error { s.closed = true; return nil }

func TestBorrowedRegionReaderAtOwnershipAndLazyOpening(t *testing.T) {
	path, _, _ := regionFixture(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stat, _ := f.Stat()
	source := &countedSource{source: f}
	r, err := OpenReaderAt(source, stat.Size(), Options{MaxOpenShards: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.OpenShards() != 0 {
		t.Fatal("eager states")
	}
	hits, err := r.Lookup(context.Background(), .75, .75)
	if err != nil || len(hits) != 2 {
		t.Fatalf("borrowed lookup: %+v %v", hits, err)
	}
	if r.OpenShards() != 1 {
		t.Fatal("state cache bound")
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if source.closed {
		t.Fatal("closed borrowed source")
	}
	if _, err := r.Lookup(context.Background(), .75, .75); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed reader usable")
	}
}
func TestBorrowedStandaloneAndInvalidSource(t *testing.T) {
	path := fixture(t, "NV", 0, 1)
	f, _ := os.Open(path)
	defer f.Close()
	st, _ := f.Stat()
	r, err := OpenReaderAt(f, st.Size(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if hits, err := r.FindID("division", "NVshared"); err != nil || len(hits) != 1 {
		t.Fatalf("standalone: %+v %v", hits, err)
	}
	if _, err := OpenReaderAt(nil, 100, Options{}); err == nil {
		t.Fatal("nil source")
	}
	if _, err := OpenReaderAt(f, -1, Options{}); err == nil {
		t.Fatal("negative size")
	}
	if _, err := OpenReaderAt(f, st.Size(), Options{MaxOpenShards: 65}); err == nil {
		t.Fatal("invalid cache")
	}
}

func TestIDPrefilterKeepsAuditAndAvoidsUnrelatedWildcardStates(t *testing.T) {
	path, _, _ := regionFixture(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.FindID("division", "NVunlocated"); !errors.Is(err, plss.ErrNoGeometry) {
		t.Fatalf("audit lost: %v", err)
	}
	if r.OpenShards() != 0 {
		t.Fatal("audit-only query initialized state indexes")
	}
	if _, err := r.FindID("division", "NVimpossible"); !errors.Is(err, plss.ErrNotFound) {
		t.Fatal(err)
	}
	if r.OpenShards() != 0 {
		t.Fatal("impossible ID initialized wildcard-route states")
	}
	hits, err := r.FindID("division", "NVshared")
	if err != nil || len(hits) != 2 {
		t.Fatalf("foreign shared IDs lost: %+v %v", hits, err)
	}
	if r.OpenShards() != 2 {
		t.Fatal("actual foreign matches not opened")
	}
}
