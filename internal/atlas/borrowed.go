package atlas

import (
	"io"
	"os"

	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

// Bounds returns the conservative index extent, including every indexed layer.
func (a *Reader) Bounds() (spatialindex.Bounds, bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return spatialindex.Bounds{}, false, os.ErrClosed
	}
	if a.view != nil {
		b, ok := a.view.Bounds()
		return b, ok, nil
	}
	b, ok := a.Index.Bounds()
	return b, ok, nil
}

// ReadBounds inspects the embedded index root without loading its leaf arrays.
func ReadBounds(r io.ReaderAt, m Manifest) (spatialindex.Bounds, bool, error) {
	s := m.Sections["index"]
	return spatialindex.ReadBounds(io.NewSectionReader(r, s.Offset, s.Size), s.Size)
}

// Representative avoids loading descriptive attributes for reverse conversion.
func (a *Reader) Representative(hit Hit) (plss.Point, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return plss.Point{}, os.ErrClosed
	}
	g, err := a.Geometry.Record(hit.FeatureID)
	if err != nil {
		return plss.Point{}, err
	}
	return g.InteriorPoint()
}

func (a *Reader) HasFullAttributes() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return !a.closed && a.Attributes != nil && !a.Attributes.Metadata().Essential
}
