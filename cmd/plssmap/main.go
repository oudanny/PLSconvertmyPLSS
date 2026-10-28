// Command plssmap opens the offline WebAssembly map in the default browser.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/mapserver"
	"github.com/oudanny/PLSconvertmyPLSS/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run() error {
	data := flag.String("data", "", "atlas file; defaults to us.atlas or western-us.atlas beside the executable")
	listen := flag.String("listen", "127.0.0.1:0", "loopback address; port 0 chooses an available port")
	noBrowser := flag.Bool("no-browser", false, "print the URL without opening a browser")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	wasm, err := web.Files.Open("runtime/plss.wasm")
	if err != nil {
		return errors.New("WebAssembly assets are missing: build with go run ./internal/guibuild")
	}
	_ = wasm.Close()
	if *data == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		for _, name := range []string{"us.atlas", "western-us.atlas"} {
			candidate := filepath.Join(filepath.Dir(exe), name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				*data = candidate
				break
			}
		}
	}
	addr, err := net.ResolveTCPAddr("tcp", *listen)
	if err != nil {
		return err
	}
	if addr.IP == nil || !addr.IP.IsLoopback() {
		return errors.New("--listen must use a loopback IP address")
	}
	handler, err := mapserver.New(web.Files, *data)
	if err != nil {
		return err
	}
	defer handler.Close()
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	address := "http://" + listener.Addr().String()
	fmt.Println("PLSconvertmyPLSS map:", address)
	fmt.Println("Lookups run locally in Go WebAssembly. Keep this window open; press Ctrl+C to stop.")
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if !*noBrowser {
		if err := openBrowser(address); err != nil {
			fmt.Fprintln(os.Stderr, "Open the URL above in your browser:", err)
		}
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openBrowser(address string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	case "darwin":
		cmd = exec.Command("open", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
