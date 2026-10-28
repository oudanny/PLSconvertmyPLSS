package keyindex

import "strings"

// IDFilter can rule out impossible identifiers using only the small dictionaries.
// It deliberately admits combinations whose dictionary parts never coexist on a
// source row; actual Find still resolves records and preserves every duplicate.
// Filters are immutable and safe for concurrent use.
type IDFilter struct {
	prefixes    []string
	layers      [3]uint32
	full        [3]map[string]bool
	suffix      [3][]string
	surveys     map[string]bool
	surveyParts []SurveyPart
	exceptions  map[string]bool
}

func newIDFilter(m manifest) *IDFilter {
	f := &IDFilter{prefixes: routingPrefixes(m), layers: m.Layers, surveys: map[string]bool{}, exceptions: map[string]bool{}}
	for i, dict := range m.Dicts {
		f.full[i] = map[string]bool{}
		seen := map[string]bool{}
		for _, p := range dict {
			if i == 0 || !p.Append {
				f.full[i][p.Text] = true
			} else if !seen[p.Text] {
				f.suffix[i] = append(f.suffix[i], p.Text)
				seen[p.Text] = true
			}
		}
	}
	for _, id := range m.Surveys {
		f.surveys[id] = true
	}
	for _, p := range m.SurveyDict {
		if p.Base == 0 {
			f.surveys[p.Text] = true
		} else {
			f.surveyParts = append(f.surveyParts, p)
		}
	}
	for id := range m.Exceptions {
		f.exceptions[id] = true
	}
	return f
}

func (f *IDFilter) Prefixes() []string { return append([]string(nil), f.prefixes...) }

// LayerCounts describes the same source rows as the full packed key index.
// Container readers must validate these before using the filter to skip data.
func (f *IDFilter) LayerCounts() [3]uint32 { return f.layers }

func (f *IDFilter) MayContain(kind, id string) bool {
	// Keep the filter conservative for empty IDs and future/unknown callers;
	// the full key reader remains responsible for API validation.
	if id == "" {
		return true
	}
	field, layer := 0, 0
	switch kind {
	case "township":
	case "section":
		field, layer = 1, 1
	case "division", "intersected":
		field, layer = 2, 2
	case "survey":
		field, layer = 3, 2
	default:
		return true
	}
	if f.layers[layer] == 0 {
		return false
	}
	if f.exceptions[string(byte('1'+field))+":"+id] {
		return true
	}
	type key struct {
		field int
		id    string
	}
	known := map[key]bool{}
	values := map[key]bool{}
	var match func(int, string) bool
	match = func(field int, id string) bool {
		k := key{field, id}
		if known[k] {
			return values[k]
		}
		known[k] = true
		if f.full[field][id] {
			values[k] = true
			return true
		}
		for _, suffix := range f.suffix[field] {
			if strings.HasSuffix(id, suffix) && match(field-1, id[:len(id)-len(suffix)]) {
				values[k] = true
				return true
			}
		}
		return false
	}
	if field < 3 {
		return match(field, id)
	}
	if f.surveys[id] {
		return true
	}
	for _, part := range f.surveyParts {
		// A malformed future base must never turn this prefilter into a false
		// negative; the full index validates its actual survey representation.
		if part.Base > 3 {
			return true
		}
		if strings.HasSuffix(id, part.Text) && match(int(part.Base)-1, id[:len(id)-len(part.Text)]) {
			return true
		}
	}
	return false
}

// LayerCounts returns the exact row counts in township, section, and subdivision order.
func (x *Index) LayerCounts() [3]uint32 { return x.m.Layers }
