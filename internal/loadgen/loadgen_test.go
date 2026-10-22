package loadgen

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// newServer serves /down?bytes=N (N zero bytes, capped to keep tests quick)
// and drains POST bodies at /up. A CF-RAY header is set so Colo parsing is
// exercised on real responses.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		if n <= 0 || n > 8*1024*1024 {
			n = 8 * 1024 * 1024
		}
		w.Header().Set("CF-RAY", "abc123-EWR")
		w.WriteHeader(http.StatusOK)
		block := make([]byte, 64*1024)
		for n > 0 {
			k := len(block)
			if n < k {
				k = n
			}
			if _, err := w.Write(block[:k]); err != nil {
				return
			}
			n -= k
		}
	})
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/limited", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadCountsAndStops(t *testing.T) {
	srv := newServer(t)
	src := Source{Name: "local", DownURL: srv.URL + "/down?bytes=%d"}
	var n atomic.Uint64
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	Download(ctx, src, 3, &n)
	if n.Load() == 0 {
		t.Fatal("downloaded 0 bytes")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Download did not stop promptly after cancel: %v", el)
	}
}

func TestUploadCountsAndStops(t *testing.T) {
	srv := newServer(t)
	src := Source{Name: "local", DownURL: srv.URL + "/down?bytes=%d", UpURL: srv.URL + "/up"}
	var n atomic.Uint64
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	Upload(ctx, src, 2, &n)
	if n.Load() == 0 {
		t.Fatal("uploaded 0 bytes")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Upload did not stop promptly after cancel: %v", el)
	}
}

func TestUploadNoopWithoutURL(t *testing.T) {
	var n atomic.Uint64
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	Upload(ctx, Source{Name: "x", DownURL: "http://127.0.0.1:1/"}, 2, &n)
	if n.Load() != 0 {
		t.Fatalf("expected no upload without UpURL, got %d bytes", n.Load())
	}
}

func TestPickSourceSkipsFailures(t *testing.T) {
	srv := newServer(t)
	sources := []Source{
		{Name: "limited", DownURL: srv.URL + "/limited"},
		{Name: "dead", DownURL: "http://127.0.0.1:1/nothing"},
		{Name: "good", DownURL: srv.URL + "/down?bytes=%d", UpURL: srv.URL + "/up"},
	}
	src, colo, err := PickSource(context.Background(), sources)
	if err != nil {
		t.Fatal(err)
	}
	if src.Name != "good" {
		t.Fatalf("picked %q, want good", src.Name)
	}
	if colo != "EWR" {
		t.Fatalf("colo = %q, want EWR", colo)
	}
	if _, _, err := PickSource(context.Background(), sources[:2]); err == nil {
		t.Fatal("expected error when no source works")
	}
}

func TestColo(t *testing.T) {
	h := http.Header{}
	if Colo(h) != "" {
		t.Fatal("empty headers should yield empty colo")
	}
	h.Set("CF-RAY", "8f1c2a-iad")
	if got := Colo(h); got != "IAD" {
		t.Fatalf("CF-RAY colo = %q", got)
	}
	h.Set("cf-meta-colo", "ord")
	if got := Colo(h); got != "ORD" {
		t.Fatalf("cf-meta-colo should win, got %q", got)
	}
}

func TestMbps(t *testing.T) {
	if got := Mbps(1_000_000, time.Second); got != 8 {
		t.Fatalf("1 MB/s = %v Mbps, want 8", got)
	}
	if got := Mbps(1_000_000, 0); got != 0 {
		t.Fatalf("zero elapsed should give 0, got %v", got)
	}
}

// Cloudflare refuses __down requests above ~50 MB with HTTP 403; the chunk
// size must stay at or below that or every stream silently moves 0 bytes.
func TestDownChunkWithinCloudflareLimit(t *testing.T) {
	if DownChunkBytes > 50*1024*1024 {
		t.Fatalf("DownChunkBytes = %d exceeds Cloudflare's accepted size", DownChunkBytes)
	}
}

func TestDownURLFormatting(t *testing.T) {
	if got := (Source{DownURL: "http://x/d?bytes=%d"}).downURL(42); got != "http://x/d?bytes=42" {
		t.Fatalf("formatted URL = %q", got)
	}
	if got := (Source{DownURL: "http://x/file.bin"}).downURL(42); got != "http://x/file.bin" {
		t.Fatalf("fixed URL = %q", got)
	}
}
