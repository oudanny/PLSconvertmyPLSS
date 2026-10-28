//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strconv"
	"strings"
	"syscall/js"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/mapengine"
	"github.com/oudanny/PLSconvertmyPLSS/internal/readcache"
	"github.com/oudanny/PLSconvertmyPLSS/offline"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const maxSafeInteger = 9007199254740991

type engine struct {
	reader *offline.Reader
	source *readcache.Reader
	name   string
	size   int64
}
type envelope struct {
	OK     bool       `json:"ok"`
	Result any        `json:"result,omitempty"`
	Error  string     `json:"error,omitempty"`
	Stats  statistics `json:"stats"`
}
type statistics struct {
	readcache.Stats
	GoHeapBytes uint64  `json:"go_heap_bytes"`
	ElapsedMS   float64 `json:"elapsed_ms"`
}
type info struct {
	Name      string              `json:"name"`
	Size      int64               `json:"size"`
	Sources   []plss.PackMetadata `json:"sources"`
	States    []string            `json:"states"`
	Features  int64               `json:"features"`
	Unlocated int64               `json:"unlocated"`
	Loaded    bool                `json:"loaded"`
}

func main() {
	e := new(engine)
	callback := js.FuncOf(func(this js.Value, args []js.Value) (result any) {
		begin := time.Now()
		// Convert JavaScript exceptions (revoked file permissions, unavailable
		// APIs, failed XHR) into a visible error instead of killing the worker.
		defer func() {
			if recovered := recover(); recovered != nil {
				result = e.encode(nil, fmt.Errorf("browser file access failed: %v", recovered), begin)
			}
		}()
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return e.encode(nil, errors.New("provide an engine command"), begin)
		}
		payload := "{}"
		if len(args) > 1 && args[1].Type() == js.TypeString {
			payload = args[1].String()
		}
		var file js.Value
		if len(args) > 2 {
			file = args[2]
		}
		value, err := e.execute(args[0].String(), payload, file)
		return e.encode(value, err, begin)
	})
	js.Global().Set("plssEngine", callback)
	js.Global().Set("plssEngineReady", true)
	select {}
}

func (e *engine) encode(value any, err error, begin time.Time) string {
	out := envelope{OK: err == nil, Result: value}
	if err != nil {
		out.Error = err.Error()
		out.Result = nil
	}
	if e.source != nil {
		out.Stats.Stats = e.source.Stats()
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	out.Stats.GoHeapBytes = mem.HeapAlloc
	out.Stats.ElapsedMS = float64(time.Since(begin).Nanoseconds()) / 1e6
	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		b, _ = json.Marshal(envelope{Error: marshalErr.Error()})
	}
	return string(b)
}

func (e *engine) information() info {
	i := info{Name: e.name, Size: e.size, Loaded: e.reader != nil, States: []string{}, Sources: []plss.PackMetadata{}}
	if e.reader == nil {
		return i
	}
	i.Sources = e.reader.Sources()
	for _, s := range i.Sources {
		i.States = append(i.States, s.State)
		for _, n := range s.Counts {
			i.Features += int64(n)
		}
		for _, n := range s.UnlocatedCounts {
			i.Unlocated += int64(n)
		}
	}
	return i
}

func (e *engine) execute(command, payload string, file js.Value) (any, error) {
	switch command {
	case "open", "openURL":
		var options struct {
			URL  string  `json:"url"`
			Size float64 `json:"size"`
			Name string  `json:"name"`
		}
		if err := json.Unmarshal([]byte(payload), &options); err != nil {
			return nil, err
		}
		var source io.ReaderAt
		var size int64
		var name string
		if command == "open" {
			if file.Type() != js.TypeObject || file.Get("slice").Type() != js.TypeFunction || file.Get("size").Type() != js.TypeNumber {
				return nil, errors.New("choose a local .atlas file")
			}
			var err error
			size, err = validSize(file.Get("size").Float())
			if err != nil {
				return nil, err
			}
			ctor := js.Global().Get("FileReaderSync")
			if ctor.Type() != js.TypeFunction {
				return nil, errors.New("this browser does not support synchronous local-file reads in a dedicated worker")
			}
			name = file.Get("name").String()
			source = &localFile{file: file, reader: ctor.New()}
		} else {
			var err error
			size, err = validSize(options.Size)
			if err != nil {
				return nil, err
			}
			if options.URL == "" {
				return nil, errors.New("provide a local atlas URL")
			}
			location := js.Global().Get("location")
			url := js.Global().Get("URL").New(options.URL, location.Get("href").String())
			if url.Get("origin").String() != location.Get("origin").String() {
				return nil, errors.New("atlas URL must have the same origin as the application")
			}
			if scheme := url.Get("protocol").String(); scheme != "http:" && scheme != "https:" {
				return nil, errors.New("atlas URL must use HTTP or HTTPS")
			}
			name = options.Name
			if name == "" {
				name = "Local atlas"
			}
			source = &rangeFile{url: url.Get("href").String(), size: size}
		}
		pageSize := readcache.PageSize
		maxRead := readcache.MaxReadSize
		if command == "openURL" {
			pageSize = 512 << 10
			maxRead = readcache.MaxRangeReadSize
		}
		cache, err := readcache.NewWithOptions(source, size, readcache.Options{Pages: (8 << 20) / pageSize, PageSize: pageSize, MaxReadBytes: maxRead}) // At most 8 MiB of file pages.
		if err != nil {
			return nil, err
		}
		// The national snapshot has up to five overlapping shard rectangles,
		// including the global unassigned-record shard. Keep one spare so a
		// nearby repeated query does not evict a state it will need immediately.
		reader, err := offline.OpenReaderAt(cache, size, offline.Options{LazyStateCache: 6})
		if err != nil {
			return nil, fmt.Errorf("open atlas: %w", err)
		}
		// Keep a currently usable atlas if the replacement is invalid.
		if e.reader != nil {
			_ = e.reader.Close()
		}
		e.reader, e.source, e.name, e.size = reader, cache, name, size
		return e.information(), nil
	case "close":
		if e.reader != nil {
			_ = e.reader.Close()
		}
		e.reader, e.source = nil, nil
		e.name = ""
		e.size = 0
		return e.information(), nil
	case "info":
		return e.information(), nil
	case "convert", "lookup", "find":
		var request mapengine.Request
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			return nil, err
		}
		if command == "lookup" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal([]byte(payload), &fields)
			if len(fields["latitude"]) == 0 || len(fields["longitude"]) == 0 || string(fields["latitude"]) == "null" || string(fields["longitude"]) == "null" {
				return nil, errors.New("provide latitude and longitude")
			}
		}
		var reader mapengine.Reader
		if e.reader != nil {
			reader = e.reader
		}
		return mapengine.Execute(context.Background(), reader, command, request)
	default:
		return nil, errors.New("unknown engine command")
	}
}

func validSize(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 65536 || value > maxSafeInteger || value != math.Trunc(value) {
		return 0, errors.New("invalid atlas file size")
	}
	return int64(value), nil
}

type localFile struct{ file, reader js.Value }

func (f *localFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || len(p) > readcache.MaxReadSize {
		return 0, errors.New("invalid browser read range")
	}
	if len(p) == 0 {
		return 0, nil
	}
	blob := f.file.Call("slice", float64(off), float64(off)+float64(len(p)))
	buffer := f.reader.Call("readAsArrayBuffer", blob)
	array := js.Global().Get("Uint8Array").New(buffer)
	n := js.CopyBytesToGo(p, array)
	if n != len(p) {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

type rangeFile struct {
	url  string
	size int64
}

func (f *rangeFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || len(p) > readcache.MaxRangeReadSize {
		return 0, errors.New("invalid browser read range")
	}
	if len(p) == 0 {
		return 0, nil
	}
	xhr := js.Global().Get("XMLHttpRequest").New()
	xhr.Call("open", "GET", f.url, false)
	xhr.Set("responseType", "arraybuffer")
	end := off + int64(len(p)) - 1
	xhr.Call("setRequestHeader", "Range", fmt.Sprintf("bytes=%d-%d", off, end))
	xhr.Call("send")
	if xhr.Get("status").Int() != 206 {
		return 0, fmt.Errorf("atlas server must support byte ranges (HTTP %d)", xhr.Get("status").Int())
	}
	contentRange := xhr.Call("getResponseHeader", "Content-Range").String()
	expected := "bytes " + strconv.FormatInt(off, 10) + "-" + strconv.FormatInt(end, 10) + "/" + strconv.FormatInt(f.size, 10)
	if strings.TrimSpace(contentRange) != expected {
		return 0, errors.New("atlas server returned an unexpected byte range; the file may have changed")
	}
	array := js.Global().Get("Uint8Array").New(xhr.Get("response"))
	if array.Get("byteLength").Int() != len(p) {
		return 0, io.ErrUnexpectedEOF
	}
	return js.CopyBytesToGo(p, array), nil
}
