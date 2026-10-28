package keyindex

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

// RoutingPrefix returns a two-letter uppercase ID prefix. Asterisk means the
// identifier cannot safely be routed by a state-like prefix.
func RoutingPrefix(id string) string {
	if len(id) < 2 || id[0] < 'A' || id[0] > 'Z' || id[1] < 'A' || id[1] > 'Z' {
		return "*"
	}
	return id[:2]
}

// RoutingPrefixes conservatively covers every identifier represented by the
// dictionaries, including independent division prefixes and survey IDs. It
// may include extra routes; it must never exclude a real published ID.
func (x *Index) RoutingPrefixes() []string { return routingPrefixes(x.m) }

func routingPrefixes(m manifest) []string {
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" {
			seen[RoutingPrefix(s)] = true
		}
	}
	for _, dict := range m.Dicts {
		for _, p := range dict {
			if !p.Append {
				add(p.Text)
			}
		}
	}
	for _, s := range m.Surveys {
		add(s)
	}
	for _, p := range m.SurveyDict {
		if p.Base == 0 {
			add(p.Text)
		}
	}
	for s := range m.Exceptions {
		if len(s) > 2 {
			add(s[2:])
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ReadRoutingPrefixes reads the small dictionary header without copying packed
// feature rows or scanning postings. Full key-index validation is separate.
func ReadRoutingPrefixes(r io.ReaderAt, size int64) ([]string, error) {
	filter, err := ReadIDFilter(r, size)
	if err != nil {
		return nil, err
	}
	return filter.Prefixes(), nil
}

// ReadIDFilter reads the same validated dictionary header as routing, retaining
// a conservative field-aware filter without loading packed feature rows.
func ReadIDFilter(r io.ReaderAt, size int64) (*IDFilter, error) {
	var h [12]byte
	if size < 12 || size > 1<<30 {
		return nil, errors.New("invalid key-index size")
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return nil, err
	}
	if string(h[:8]) != "PLSSKEY1" && string(h[:8]) != "PLSSKEY2" && string(h[:8]) != "PLSSKEY3" {
		return nil, errors.New("invalid key-index magic")
	}
	n := int64(binary.LittleEndian.Uint32(h[8:]))
	if n > size-12 || n > 64<<20 {
		return nil, errors.New("invalid or excessive routing manifest")
	}
	data := make([]byte, int(n))
	if _, err := r.ReadAt(data, 12); err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Width < 1 || m.Width > 8 || uint64(m.Layers[0])+uint64(m.Layers[1])+uint64(m.Layers[2]) != uint64(m.Count) || uint64(m.Count)*uint64(m.Width) > uint64(size-12-n) {
		return nil, errors.New("invalid routing key layout")
	}
	totalBits := 0
	for i, dict := range m.Dicts {
		if len(dict) == 0 || dict[0] != (Part{}) || m.Bits[i] > 32 || uint64(len(dict)) > uint64(1)<<m.Bits[i] {
			return nil, errors.New("invalid routing dictionary")
		}
		totalBits += int(m.Bits[i])
	}
	if totalBits > int(m.Width)*8 {
		return nil, errors.New("invalid routing bit widths")
	}
	townships := map[string]bool{}
	for i, part := range m.Dicts[0] {
		if part.Append || (i > 0 && part.Text == "") {
			return nil, errors.New("invalid routing township entry")
		}
		if i > 0 {
			if townships[part.Text] {
				return nil, errors.New("duplicate routing township entry")
			}
			townships[part.Text] = true
		}
	}
	if string(h[:8]) == "PLSSKEY3" {
		x := &Index{m: m, version3: true}
		bytes, err := x.surveyDataSize()
		if err != nil {
			return nil, err
		}
		remaining := uint64(size-12-n) - uint64(m.Count)*uint64(m.Width)
		if bytes > remaining {
			return nil, errors.New("truncated routing survey codes")
		}
		if !equalRoutes(m.Routes, routingPrefixes(m)) {
			return nil, errors.New("inconsistent routing prefixes")
		}
	}
	return newIDFilter(m), nil
}

func equalRoutes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
