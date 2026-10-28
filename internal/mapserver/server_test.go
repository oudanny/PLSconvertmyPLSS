package mapserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
)

func TestConcurrentAtlasRangesAndLocalAccess(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 32768)
	path := filepath.Join(t.TempDir(), "example.atlas")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"index.html": {Data: []byte("map application")}, "runtime/plss.wasm": {Data: []byte("wasm")}, "embed.go": {Data: []byte("source")}}
	handler, err := New(files, path)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			start, end := 37*i, 37*i+8191
			req, _ := http.NewRequest("GET", server.URL+"/atlas", nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 206 || !bytes.Equal(body, data[start:end+1]) {
				t.Errorf("range %d failed: status=%d err=%v", i, response.StatusCode, err)
			}
			if got, want := response.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)); got != want {
				t.Errorf("range header %q != %q", got, want)
			}
		}(i)
	}
	group.Wait()
	for _, test := range []struct {
		path, method, host, origin string
		want                       int
	}{
		{"/", "GET", "localhost:1234", "", 200},
		{"/atlas", "GET", "attacker.example", "", 403},
		{"/atlas", "GET", "127.0.0.1:1234", "https://unrelated.example", 403},
		{"/atlas", "GET", "127.0.0.1:1234", "http://127.0.0.1:1234", 200},
		{"/atlas", "POST", "127.0.0.1:1234", "", 405},
		{"/embed.go", "GET", "127.0.0.1:1234", "", 404},
		{"/runtime/plss.wasm", "GET", "127.0.0.1:1234", "", 200},
	} {
		req := httptest.NewRequest(test.method, "http://localhost"+test.path, nil)
		req.Host = test.host
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != test.want {
			t.Errorf("%+v: got %d", test, recorder.Code)
		}
		if test.path == "/runtime/plss.wasm" && recorder.Header().Get("Content-Type") != "application/wasm" {
			t.Error("incorrect WASM media type")
		}
	}
}

func TestNoAtlasAndConfigPrivacy(t *testing.T) {
	handler, err := New(fstest.MapFS{}, "")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://localhost/api/config", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var config map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config["atlas"] != nil {
		t.Fatal("missing atlas should be explicit")
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "http://localhost/atlas", nil))
	if recorder.Code != 404 {
		t.Fatal(recorder.Code)
	}
}
