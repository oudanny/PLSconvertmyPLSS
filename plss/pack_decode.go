package plss

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// A reader's DEFLATE window costs about 40 KiB to initialize. Keep independent
// readers for simultaneous queries, without caching mutable returned features.
// Pool entries may be reclaimed by the runtime at any garbage collection.
var packDecoders sync.Pool

type packDecoder struct {
	source bytes.Reader
	zip    io.ReadCloser
	raw    bytes.Buffer
}

func decodePackFeature(data []byte) (packFeature, error) {
	d, _ := packDecoders.Get().(*packDecoder)
	if d == nil {
		d = new(packDecoder)
	}
	d.source.Reset(data)
	d.raw.Reset()
	var err error
	if d.zip == nil {
		d.zip, err = zlib.NewReader(&d.source)
	} else {
		err = d.zip.(zlib.Resetter).Reset(&d.source, nil)
	}
	if err != nil {
		return packFeature{}, fmt.Errorf("corrupt offline feature: %w", err)
	}
	_, err = d.raw.ReadFrom(io.LimitReader(d.zip, maxPackFeatureBytes+1))
	closeErr := d.zip.Close()
	if err != nil {
		return packFeature{}, fmt.Errorf("corrupt offline feature: %w", err)
	}
	if closeErr != nil {
		return packFeature{}, fmt.Errorf("corrupt offline feature: %w", closeErr)
	}
	if d.raw.Len() > maxPackFeatureBytes {
		return packFeature{}, errors.New("offline feature exceeds 64 MiB")
	}
	var feature packFeature
	decoder := json.NewDecoder(bytes.NewReader(d.raw.Bytes()))
	decoder.UseNumber()
	err = decoder.Decode(&feature)
	// Neither the decompressor nor the scratch buffer owns returned attributes
	// or vertices. Avoid retaining a source blob or an unusually large polygon.
	d.source.Reset(nil)
	if d.raw.Cap() > 128<<10 {
		d.raw = bytes.Buffer{}
	}
	packDecoders.Put(d)
	if err != nil {
		return packFeature{}, fmt.Errorf("corrupt offline feature JSON: %w", err)
	}
	return feature, nil
}
