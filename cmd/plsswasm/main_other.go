//go:build !js

// Command plsswasm builds the Go lookup engine for a dedicated browser worker.
package main

import "fmt"

func main() { fmt.Println("Build this command with GOOS=js GOARCH=wasm for the map application.") }
