// Command guibuild compiles matching WASM/runtime assets and embeds them in the
// native, portable launcher. Run it from the repository root.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	output := flag.String("out", "", "output launcher (default bin/plssmap[.exe])")
	flag.Parse()
	if err := build(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(output string) error {
	if _, err := os.Stat("go.mod"); err != nil {
		return fmt.Errorf("run guibuild from the repository root: %w", err)
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}
	env := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GOOS=") && !strings.HasPrefix(item, "GOARCH=") && !strings.HasPrefix(item, "CGO_ENABLED=") {
			env = append(env, item)
		}
	}
	if err := os.MkdirAll("web/runtime", 0755); err != nil {
		return err
	}
	wasm := exec.Command(goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", "web/runtime/plss.wasm", "./cmd/plsswasm")
	wasm.Env = append(append([]string{}, env...), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	wasm.Stdout, wasm.Stderr = os.Stdout, os.Stderr
	if err := wasm.Run(); err != nil {
		return fmt.Errorf("build WASM: %w", err)
	}
	runtimePath := filepath.Join(runtime.GOROOT(), "lib", "wasm", "wasm_exec.js")
	content, err := os.ReadFile(runtimePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile("web/runtime/wasm_exec.js", content, 0644); err != nil {
		return err
	}
	license, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		return err
	}
	if err := os.WriteFile("web/runtime/GO-LICENSE.txt", license, 0644); err != nil {
		return err
	}
	if output == "" {
		output = "bin/plssmap"
		targetOS := os.Getenv("GOOS")
		if targetOS == "" {
			targetOS = runtime.GOOS
		}
		if targetOS == "windows" {
			output += ".exe"
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	native := exec.Command(goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", output, "./cmd/plssmap")
	// The requested GOOS/GOARCH, when set, apply only to the native launcher.
	native.Env = append(os.Environ(), "CGO_ENABLED=0")
	native.Stdout, native.Stderr = os.Stdout, os.Stderr
	if err := native.Run(); err != nil {
		return fmt.Errorf("build launcher: %w", err)
	}
	fmt.Println("Built", output)
	return nil
}
