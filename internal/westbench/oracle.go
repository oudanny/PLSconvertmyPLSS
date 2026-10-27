package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"

	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func identity(state, layer string, row uint32, id string) string {
	return fmt.Sprintf("%s:%s:%d:%s", state, layer, row, id)
}
func layerName(layer int) string { return []string{"", "township", "section", "intersected"}[layer] }
func publishedID(town, first, second, survey string) string {
	for _, id := range []string{second, survey, first, town} {
		if id != "" {
			return id
		}
	}
	return ""
}

func decodeGeometry(blob []byte) (plss.Geometry, error) {
	z, err := zlib.NewReader(bytes.NewReader(blob))
	if err != nil {
		return plss.Geometry{}, err
	}
	defer z.Close()
	raw, err := io.ReadAll(io.LimitReader(z, (64<<20)+1))
	if err != nil {
		return plss.Geometry{}, err
	}
	if len(raw) > 64<<20 {
		return plss.Geometry{}, errors.New("source feature exceeds64MiB")
	}
	var feature struct {
		Geometry plss.Geometry `json:"geometry"`
	}
	if err := json.Unmarshal(raw, &feature); err != nil {
		return plss.Geometry{}, err
	}
	if len(feature.Geometry.Rings) == 0 {
		return plss.Geometry{}, errors.New("source geometry has no rings")
	}
	for _, ring := range feature.Geometry.Rings {
		if len(ring) < 3 {
			return plss.Geometry{}, errors.New("source geometry has short ring")
		}
		for _, v := range ring {
			if len(v) < 2 {
				return plss.Geometry{}, errors.New("source vertex has fewer than2 ordinates")
			}
		}
	}
	return feature.Geometry, nil
}

// The oracle uses source SQL row identities and source polygons. It does not
// use the packed atlas index, vertex codec, key dictionary, or route manifest.
// Identical published IDs still identify separate source records.
func sourceMatches(ctx context.Context, sources []source, p plss.Point) ([]string, error) {
	for layer := 3; layer >= 1; layer-- {
		out := []string{}
		for _, s := range sources {
			if p.Longitude < s.bounds[0] || p.Longitude > s.bounds[1] || p.Latitude < s.bounds[2] || p.Latitude > s.bounds[3] {
				continue
			}
			found, err := sourceLayerMatches(ctx, s, p, layer)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out, nil
		}
	}
	return []string{}, nil
}

func sourceLayerMatches(ctx context.Context, s source, p plss.Point, layer int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,plssid,frstdivid,secdivid,survid,data FROM features WHERE layer=? AND id IN (SELECT feature_id FROM spatial WHERE min_lon<=? AND max_lon>=? AND min_lat<=? AND max_lat>=? AND layer=?)`, layer, p.Longitude, p.Longitude, p.Latitude, p.Latitude, layer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id uint32
		var town, first, second, survey string
		var blob []byte
		if err := rows.Scan(&id, &town, &first, &second, &survey, &blob); err != nil {
			return nil, err
		}
		g, err := decodeGeometry(blob)
		if err != nil {
			return nil, err
		}
		inside, err := g.ContainsPoint(p)
		if err != nil {
			return nil, err
		}
		if inside {
			out = append(out, identity(s.state, layerName(layer), id, publishedID(town, first, second, survey)))
		}
	}
	return out, rows.Err()
}

func boundarySamples(g plss.Geometry, rng *rand.Rand) []plss.Point {
	ring := g.Rings[rng.Intn(len(g.Rings))]
	i := rng.Intn(len(ring))
	v, w := ring[i], ring[(i+1)%len(ring)]
	dx, dy := w[0]-v[0], w[1]-v[1]
	if dx > 180 {
		dx -= 360
	} else if dx < -180 {
		dx += 360
	}
	wrap := func(lon float64) float64 {
		if lon > 180 {
			return lon - 360
		}
		if lon < -180 {
			return lon + 360
		}
		return lon
	}
	mid := plss.Point{Longitude: wrap(v[0] + dx/2), Latitude: v[1] + dy/2}
	out := []plss.Point{{Longitude: v[0], Latitude: v[1]}, mid}
	if distance := math.Hypot(dx, dy); distance != 0 {
		for _, sign := range []float64{-1, 1} {
			p := plss.Point{Longitude: wrap(mid.Longitude - sign*dy/distance*1e-10), Latitude: mid.Latitude + sign*dx/distance*1e-10}
			if p.Latitude >= -90 && p.Latitude <= 90 {
				out = append(out, p)
			}
		}
	}
	return out
}

func sourceReverse(ctx context.Context, sources []source, kind, value string) ([]string, error) {
	field, layer := "plssid", 1
	switch kind {
	case "township":
	case "section":
		field, layer = "frstdivid", 2
	case "division", "intersected":
		field, layer = "secdivid", 3
	case "survey":
		field, layer = "survid", 3
	default:
		return nil, errors.New("invalid reverse kind")
	}
	out := []string{}
	unlocated := false
	for _, s := range sources {
		rows, err := s.db.QueryContext(ctx, "SELECT id,plssid,frstdivid,secdivid,survid FROM features WHERE layer=? AND "+field+"=?", layer, value)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id uint32
			var town, first, second, survey string
			if err := rows.Scan(&id, &town, &first, &second, &survey); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, identity(s.state, layerName(layer), id, publishedID(town, first, second, survey)))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if s.metadata.UnlocatedCounts[layerName(layer)] > 0 {
			var count int
			if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM unlocated WHERE layer=? AND "+field+"=?", layer, value).Scan(&count); err != nil {
				return nil, err
			}
			unlocated = unlocated || count > 0
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return out, nil
	}
	if unlocated {
		return out, plss.ErrNoGeometry
	}
	return out, plss.ErrNotFound
}

func checkReverse(ctx context.Context, a *regionatlas.Reader, sources []source, kind, value string) error {
	actual, gotErr := a.FindID(kind, value)
	want, wantErr := sourceReverse(ctx, sources, kind, value)
	class := func(err error) string {
		if err == nil {
			return "found"
		}
		if errors.Is(err, plss.ErrNotFound) {
			return "missing"
		}
		if errors.Is(err, plss.ErrNoGeometry) {
			return "unlocated"
		}
		return err.Error()
	}
	if class(gotErr) != class(wantErr) {
		return fmt.Errorf("reverse %s %q error class differs: %v != %v", kind, value, gotErr, wantErr)
	}
	if gotErr != nil && !errors.Is(gotErr, plss.ErrNotFound) && !errors.Is(gotErr, plss.ErrNoGeometry) {
		return gotErr
	}
	got := actualKeys(actual)
	if len(got) != len(want) {
		return fmt.Errorf("reverse %s %q record count differs: %v != %v", kind, value, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("reverse %s %q source row differs: %v != %v", kind, value, got, want)
		}
	}
	return nil
}

type reverseQuery struct{ kind, value string }

func explicitReverseSamples(s source) ([]reverseQuery, error) {
	var out []reverseQuery
	// The partial survey index makes this bounded even when almost all source
	// rows have an empty survey ID. Random rows alone rarely exercise surveys.
	rows, err := s.db.Query("SELECT DISTINCT survid FROM features WHERE layer=3 AND survid>'' ORDER BY survid LIMIT 20")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, reverseQuery{"survey", value})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var unlocated int
	for _, count := range s.metadata.UnlocatedCounts {
		unlocated += count
	}
	if unlocated == 0 {
		return out, nil
	}
	rows, err = s.db.Query("SELECT plssid,frstdivid,secdivid,survid FROM unlocated ORDER BY id LIMIT 20")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var town, first, second, survey string
		if err := rows.Scan(&town, &first, &second, &survey); err != nil {
			return nil, err
		}
		for _, q := range []reverseQuery{{"township", town}, {"section", first}, {"division", second}, {"survey", survey}} {
			if q.value != "" {
				out = append(out, q)
			}
		}
	}
	return out, rows.Err()
}
