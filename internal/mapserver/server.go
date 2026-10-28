// Package mapserver serves only the embedded UI and a selected, read-only atlas.
// Coordinate conversion and polygon queries run in the browser's Go WASM worker.
package mapserver

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Server struct {
	static http.Handler
	file   *os.File
	info   os.FileInfo
}

func New(files fs.FS, atlasPath string) (*Server, error) {
	s := &Server{static: http.FileServer(http.FS(files))}
	if atlasPath != "" {
		f, err := os.Open(atlasPath)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			if err == nil {
				err = errors.New("atlas must be a regular file")
			}
			return nil, err
		}
		s.file, s.info = f, info
	}
	return s, nil
}

func (s *Server) Close() error {
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

func localHost(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		host = value
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Refuse DNS-rebinding hostnames and requests made by unrelated websites.
	if !localHost(r.Host) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || u.Host != r.Host {
			http.Error(w, "same-origin requests only", http.StatusForbidden)
			return
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only server", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/api/config":
		config := map[string]any{"application": "PLSconvertmyPLSS", "engine": "Go WebAssembly", "atlas": nil}
		if s.file != nil {
			config["atlas"] = map[string]any{"name": filepath.Base(s.info.Name()), "size": s.info.Size(), "url": "/atlas"}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(config)
		}
	case "/atlas":
		if s.file == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		// Each request has its own seek cursor over the same immutable file.
		http.ServeContent(w, r, s.info.Name(), s.info.ModTime(), io.NewSectionReader(s.file, 0, s.info.Size()))
	default:
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		// Do not expose source files accidentally included alongside embedded assets.
		if r.URL.Path == "/embed.go" {
			http.NotFound(w, r)
			return
		}
		s.static.ServeHTTP(w, r)
	}
}
