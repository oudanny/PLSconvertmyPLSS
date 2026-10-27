package atlasbuild

import (
	"errors"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
)

func newGeometryBuilder(path string, compressed, packed bool) (*compactgeom.Builder, error) {
	if compressed && packed {
		return nil, errors.New("select one vertex codec")
	}
	if packed {
		return compactgeom.NewBuilderWithOptions(path, compactgeom.BuilderOptions{VertexCodec: compactgeom.VertexXORBitpack, VertexBlockSize: 256})
	}
	if compressed {
		return compactgeom.NewBuilderWithOptions(path, compactgeom.BuilderOptions{VertexCodec: compactgeom.VertexXORShuffleZlib, VertexBlockSize: 256})
	}
	return compactgeom.NewBuilder(path, 0)
}
