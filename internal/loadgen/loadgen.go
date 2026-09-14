// Package loadgen generates saturating HTTP traffic for the on-demand tests
// (bufferbloat, speed test). It keeps the "how do we fill the pipe" concern in
// one place: choosing a public endpoint that actually serves non-browser
// clients, running parallel download streams, and pushing parallel upload
// streams — while callers only count bytes and watch latency.
package loadgen

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// UserAgent is sent with every load request; some CDNs reject Go's default
// user agent with HTTP 403.
const UserAgent = "AgentSmith/1.0 (+https://github.com/NYBaywatch/agent-smith)"

// Source is a traffic-generator endpoint pair.
type Source struct {
	Name    string
	DownURL string // Go format string with one %d for the byte count, or a fixed URL when it has no %d
	UpURL   string // "" when upload is unsupported
}

// DefaultSources lists public endpoints known to serve parallel streams to
// non-browser clients. Cloudflare's speed endpoints come first because they
// support arbitrary sizes and upload; the plain file host is a fallback.
var DefaultSources = []Source{
	{Name: "Cloudflare", DownURL: "https://speed.cloudflare.com/__down?bytes=%d", UpURL: "https://speed.cloudflare.com/__up"},
	{Name: "CacheFly", DownURL: "https://cachefly.cachefly.net/100mb.test"},
	{Name: "ThinkBroadband", DownURL: "http://ipv4.download.thinkbroadband.com/50MB.zip"},
	{Name: "Hetzner", DownURL: "https://ash-speed.hetzner.com/100MB.bin"},
}

// Chunk sizes per request. Cloudflare's __down endpoint answers 403 above
// roughly 50 MB, so downloads re-request in 50 MB chunks; streams re-issue
// requests back-to-back, so the chunk size does not bound throughput.
const (
	DownChunkBytes = 50 * 1024 * 1024 // per GET for %d sources
	upChunkBytes   = 50 * 1024 * 1024 // per POST before re-posting
	blockSize      = 64 * 1024
)

// downURL renders src.DownURL for a request of n bytes.
func (s Source) downURL(n int) string {
	if strings.Contains(s.DownURL, "%d") {
		return fmt.Sprintf(s.DownURL, n)
	}
	return s.DownURL
}

// newTransport returns a transport tuned for load generation: one TCP
// connection per stream (no HTTP/2 multiplexing, which would collapse the
// parallel streams onto a single connection) and no compression overhead.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{}, // disable h2
		DisableCompression:  true,
		DisableKeepAlives:   false,
		MaxIdleConnsPerHost: 64,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// PickSource returns the first source whose DownURL answers 200 with a readable
// body (bounded to 5 s per candidate), plus the CDN colo parsed from CF-RAY
// ("IAD") or cf-meta-colo when present.
func PickSource(ctx context.Context, sources []Source) (Source, string, error) {
	tr := newTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	var lastErr error
	for _, s := range sources {
		if ctx.Err() != nil {
			return Source{}, "", ctx.Err()
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		// Probe with the real chunk size: a CDN can serve a small request while
		// rate-limiting the large streams the load phase will actually open.
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, s.downURL(DownChunkBytes), nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", UserAgent)
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		ok := resp.StatusCode == http.StatusOK
		if ok {
			buf := make([]byte, 32*1024)
			n, _ := io.ReadFull(resp.Body, buf)
			ok = n > 0
		} else {
			lastErr = fmt.Errorf("%s: HTTP %d", s.Name, resp.StatusCode)
		}
		colo := Colo(resp.Header)
		resp.Body.Close()
		cancel()
		if ok {
			return s, colo, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no sources configured")
	}
	return Source{}, "", fmt.Errorf("loadgen: no reachable download endpoint (tried %d): %w", len(sources), lastErr)
}

// Colo extracts the CDN point of presence from response headers: the suffix of
// CF-RAY ("abc-EWR" → "EWR") or cf-meta-colo. Empty when unknown.
func Colo(h http.Header) string {
	if c := strings.TrimSpace(h.Get("cf-meta-colo")); c != "" {
		return strings.ToUpper(c)
	}
	if ray := h.Get("CF-RAY"); ray != "" {
		if i := strings.LastIndex(ray, "-"); i >= 0 && i+1 < len(ray) {
			return strings.ToUpper(ray[i+1:])
		}
	}
	return ""
}

// Download runs conns parallel GET streams against src until ctx is done,
// adding bytes read to counter. Each stream requests a large chunk and
// re-requests when a response ends. It never returns an error: it simply
// stops when ctx is cancelled.
func Download(ctx context.Context, src Source, conns int, counter *atomic.Uint64) {
	if conns < 1 {
		conns = 1
	}
	tr := newTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	var wg sync.WaitGroup
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, blockSize)
			for ctx.Err() == nil {
				if !downloadOnce(ctx, client, src.downURL(DownChunkBytes), buf, counter) {
					// A failing endpoint should not spin; back off briefly.
					sleep(ctx, 250*time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
}

// downloadOnce streams one response to completion (or cancellation). It
// reports whether the request produced any bytes.
func downloadOnce(ctx context.Context, client *http.Client, url string, buf []byte, counter *atomic.Uint64) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var got uint64
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			counter.Add(uint64(n))
			got += uint64(n)
		}
		if err != nil {
			return got > 0
		}
	}
}

// Upload runs conns parallel POSTs to src.UpURL until ctx is done, adding the
// bytes handed to the transport to counter. Each POST streams a zero-filled
// chunked body from a pipe, closes it after a fixed chunk and re-posts. It is
// a no-op when the source has no upload URL.
func Upload(ctx context.Context, src Source, conns int, counter *atomic.Uint64) {
	if src.UpURL == "" {
		return
	}
	if conns < 1 {
		conns = 1
	}
	tr := newTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	var wg sync.WaitGroup
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if !uploadOnce(ctx, client, src.UpURL, counter) {
					sleep(ctx, 250*time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
}

// uploadOnce POSTs one chunk. Bytes are counted as the transport accepts them
// from the pipe, which tracks socket writes closely enough for throughput.
func uploadOnce(ctx context.Context, client *http.Client, url string, counter *atomic.Uint64) bool {
	pr, pw := io.Pipe()
	var wrote atomic.Uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		block := make([]byte, blockSize)
		var sent int
		for sent < upChunkBytes && ctx.Err() == nil {
			n, err := pw.Write(block)
			if n > 0 {
				counter.Add(uint64(n))
				wrote.Add(uint64(n))
				sent += n
			}
			if err != nil {
				break
			}
		}
		pw.Close()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		pr.Close()
		<-done
		return false
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		pr.Close()
		<-done
		return wrote.Load() > 0
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	pr.Close()
	<-done
	return resp.StatusCode < 400 && wrote.Load() > 0
}

// Mbps converts a byte count over an elapsed duration to megabits per second.
func Mbps(bytes uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes) * 8 / 1e6 / elapsed.Seconds()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Without returns sources minus the one with the given name (used to fall
// back to the next source when one turned out to be throttled).
func Without(sources []Source, name string) []Source {
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		if s.Name != name {
			out = append(out, s)
		}
	}
	return out
}
