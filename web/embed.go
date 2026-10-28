// Package web contains the local map application. Generate the matching Go
// WebAssembly runtime before building a distributable plssmap executable.
package web

import "embed"

//go:embed index.html app.css app.js geometry.js worker.js vendor data runtime
var Files embed.FS
