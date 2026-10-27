package plss

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func compressedPackJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var data bytes.Buffer
	z, err := zlib.NewWriterLevel(&data, zlib.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func samplePackJSON(label string) []byte {
	raw, err := json.Marshal(packFeature{
		Attributes: map[string]any{
			"label":   label,
			"integer": json.Number("9007199254740993"),
			"nested":  map[string]any{"items": []any{"original", nil}},
		},
		Geometry: Geometry{Rings: [][][]float64{{
			{-119.12345678912345, 39.5, 3}, {-119.1, 39.5, 4},
			{-119.1, 39.6, 5}, {-119.12345678912345, 39.5, 3},
		}}},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func TestDecodePackFeatureOwnsReturnedData(t *testing.T) {
	raw := samplePackJSON("retained feature")
	data := compressedPackJSON(t, raw)
	retained, err := decodePackFeature(data)
	if err != nil {
		t.Fatal(err)
	}
	// Returned attributes and vertices must survive changes to the input blob
	// and many subsequent uses of a pooled decoder and its scratch buffer.
	for i := range data {
		data[i] = 0
	}
	for i := 0; i < 200; i++ {
		nextData := compressedPackJSON(t, samplePackJSON(strings.Repeat("replacement", i%40+1)))
		next, err := decodePackFeature(nextData)
		if err != nil {
			t.Fatal(err)
		}
		next.Attributes["nested"].(map[string]any)["items"].([]any)[0] = "mutated"
		next.Geometry.Rings[0][0][0] = 0
	}
	got, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("retained feature changed:\n%s\nwant:\n%s", got, raw)
	}
	if retained.Attributes["integer"] != json.Number("9007199254740993") {
		t.Fatalf("integer precision changed: %v", retained.Attributes["integer"])
	}
}

func TestDecodePackFeatureConcurrentIndependence(t *testing.T) {
	const workers = 12
	data := make([][]byte, workers)
	for i := range data {
		data[i] = compressedPackJSON(t, samplePackJSON(fmt.Sprintf("worker-%d", i)))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < 100; iteration++ {
				feature, err := decodePackFeature(data[id])
				if err != nil {
					t.Errorf("worker %d: %v", id, err)
					return
				}
				if feature.Attributes["label"] != fmt.Sprintf("worker-%d", id) || feature.Geometry.Rings[0][0][0] != -119.12345678912345 {
					t.Errorf("worker %d received another call's data: %+v", id, feature)
					return
				}
				feature.Attributes["label"] = "changed by caller"
				feature.Geometry.Rings[0][0][0] = float64(id)
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestDecodePackFeatureFailureDoesNotPoisonPool(t *testing.T) {
	raw := samplePackJSON("valid after failure")
	valid := compressedPackJSON(t, raw)
	checksum := append([]byte(nil), valid...)
	checksum[len(checksum)-1] ^= 0xff
	for _, tc := range []struct {
		name    string
		data    []byte
		message string
	}{
		{"invalid header", []byte("not zlib"), "corrupt offline feature"},
		{"truncated body", valid[:len(valid)/2], "corrupt offline feature"},
		{"truncated checksum", valid[:len(valid)-2], "corrupt offline feature"},
		{"checksum mismatch", checksum, "corrupt offline feature"},
		{"invalid JSON", compressedPackJSON(t, []byte(`{"attributes":`)), "corrupt offline feature JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodePackFeature(valid); err != nil {
				t.Fatal(err)
			}
			if _, err := decodePackFeature(tc.data); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("unexpected decode failure: %v", err)
			}
			feature, err := decodePackFeature(valid)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(feature)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, raw) {
				t.Fatalf("subsequent valid decode changed: %s", got)
			}
		})
	}
}

func TestPackConcurrentQueriesOwnReturnedData(t *testing.T) {
	_, pack := createTestPack(t)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				matches, err := pack.FindID(context.Background(), "division", "shared-id")
				if err != nil {
					t.Error(err)
					return
				}
				if len(matches) != 2 || matches[0].ID() != "shared-id" || matches[0].Geometry.Rings[0][0][0] != 0 {
					t.Errorf("another caller changed this query's result: %+v", matches)
					return
				}
				matches[0].Attributes["SECDIVID"] = "caller mutation"
				matches[0].Geometry.Rings[0][0][0] = -100
			}
		}()
	}
	wg.Wait()
	final, err := pack.FindID(context.Background(), "division", "shared-id")
	if err != nil || len(final) != 2 || final[0].ID() != "shared-id" || final[0].Geometry.Rings[0][0][0] != 0 {
		t.Fatalf("concurrent reads changed stored or pooled results: %+v, %v", final, err)
	}
}

// spaceReader lets the size-limit test create a highly compressible oversized
// stream without allocating an additional 64 MiB source byte slice.
type spaceReader struct{}

func (spaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

func TestDecodePackFeatureExpansionLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("64 MiB decompression limit test")
	}
	var data bytes.Buffer
	z, err := zlib.NewWriterLevel(&data, zlib.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(z, spaceReader{}, maxPackFeatureBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decodePackFeature(data.Bytes()); err == nil || !strings.Contains(err.Error(), "exceeds 64 MiB") {
		t.Fatalf("expanded-size bound not enforced: %v", err)
	}
	// A size failure must not leave unusable reader state for the next lookup.
	if _, err := decodePackFeature(compressedPackJSON(t, samplePackJSON("after oversized stream"))); err != nil {
		t.Fatal(err)
	}
}
