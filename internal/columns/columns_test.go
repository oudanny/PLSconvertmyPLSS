package columns

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestArchiveExactAndEssential(t *testing.T) {
	for _, dict := range []bool{false, true} {
		for _, ess := range []bool{false, true} {
			path := filepath.Join(t.TempDir(), "test.cols")
			f, e := os.Create(path)
			if e != nil {
				t.Fatal(e)
			}
			w, e := NewWriter(f, Options{BlockSize: 2, Dictionary: dict, Essential: ess, Verify: true})
			if e != nil {
				t.Fatal(e)
			}
			records := []Record{{ID: 3, Layer: 3, Attributes: map[string]json.RawMessage{"OBJECTID": []byte(`9007199254740993`), "SECDIVID": []byte(`"NV21001é"`), "SECDIVNOTE": []byte(`null`), "extra": []byte(`{"n":9007199254740995}`)}}, {ID: 4, Layer: 3, Attributes: map[string]json.RawMessage{"OBJECTID": []byte(`17`), "SECDIVID": []byte(`"NV21001\\quote\""`)}}, {ID: 9, Layer: 2, Attributes: map[string]json.RawMessage{"OBJECTID": []byte(`1.2300e+2`), "FRSTDIVID": []byte(`"section"`)}}}
			for _, r := range records {
				if e := w.Add(r); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := w.Finish(); e != nil {
				t.Fatal(e)
			}
			info, _ := f.Stat()
			r, e := OpenReader(f, info.Size())
			if e != nil {
				t.Fatal(e)
			}
			encoded, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			embedded := append(make([]byte, 137), encoded...)
			section := io.NewSectionReader(bytes.NewReader(embedded), 137, int64(len(encoded)))
			if _, e := OpenReader(section, int64(len(encoded))); e != nil {
				t.Fatalf("embedded archive: %v", e)
			}
			for _, rec := range records {
				got, e := r.Attributes(rec.ID)
				if e != nil {
					t.Fatal(e)
				}
				for key, value := range rec.Attributes {
					if ess && !essential[key] {
						if _, ok := got[key]; ok {
							t.Fatal("extra field retained in essential mode")
						}
						continue
					}
					want, e := decodeValue(value)
					if e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(got[key], want) {
						t.Fatalf("%s: %#v != %#v", key, got[key], want)
					}
				}
			}
			missing, _ := r.Attributes(4)
			if _, ok := missing["SECDIVNOTE"]; ok {
				t.Fatal("missing key converted to null")
			}
			if _, e := r.Attributes(5); e == nil {
				t.Fatal("missing ID accepted")
			}
			if e := r.SetCacheBlocks(0); e != nil {
				t.Fatal(e)
			}
			if _, e := r.Attributes(3); e != nil {
				t.Fatal(e)
			}
			f.Close()
		}
	}
}
