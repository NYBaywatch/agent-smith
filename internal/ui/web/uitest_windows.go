//go:build windows && uitest

package web

import (
	"context"
	"io"
	"net"
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// This file only exists in `-tags uitest` builds. It opens a loopback control
// endpoint so a test script can inject JavaScript into the page (switch tabs,
// open sheets) while the window is captured with PrintWindow. It is never
// compiled into release binaries.
func init() {
	uiTestHook = func(ctx context.Context) {
		ln, err := net.Listen("tcp", "127.0.0.1:47831")
		if err != nil {
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/eval", func(w http.ResponseWriter, r *http.Request) {
			js, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			runtime.WindowExecJS(ctx, string(js))
			w.WriteHeader(http.StatusNoContent)
		})
		go func() { _ = http.Serve(ln, mux) }()
	}
}
