package speedtest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/loadgen"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// fakePinger answers every echo with a fixed RTT.
type fakePinger struct {
	mu   sync.Mutex
	rtt  time.Duration
	sent int
}

func (f *fakePinger) Ping(context.Context, net.IP, time.Duration) (probe.Result, error) {
	f.mu.Lock()
	f.sent++
	f.mu.Unlock()
	return probe.Result{OK: true, RTT: f.rtt, Status: "ok"}, nil
}

func (f *fakePinger) PingTTL(context.Context, net.IP, int, time.Duration) (probe.Result, error) {
	return probe.Result{}, nil
}

func (f *fakePinger) Close() error { return nil }

// newServer serves zero bytes at /down?bytes=N and drains /up.
func newServer(t *testing.T, serveBytes bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-RAY", "test-EWR")
		w.WriteHeader(http.StatusOK)
		if !serveBytes {
			_, _ = w.Write([]byte("x")) // enough for PickSource, nothing like 2 MB
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		if n <= 0 || n > 16*1024*1024 {
			n = 16 * 1024 * 1024
		}
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func tinyOptions(srv *httptest.Server, withUpload bool) Options {
	src := loadgen.Source{Name: "local", DownURL: srv.URL + "/down?bytes=%d"}
	if withUpload {
		src.UpURL = srv.URL + "/up"
	}
	return Options{
		Sources:         []loadgen.Source{src},
		Connections:     3,
		Duration:        300 * time.Millisecond,
		WarmUp:          50 * time.Millisecond,
		PingInterval:    20 * time.Millisecond,
		PingTarget:      net.IPv4(127, 0, 0, 1),
		BaselineSamples: 3,
	}
}

func TestRunMeasuresBothDirections(t *testing.T) {
	srv := newServer(t, true)
	var mu sync.Mutex
	var phases []Phase
	var pcts []float64
	progress := func(pr Progress) {
		mu.Lock()
		defer mu.Unlock()
		if len(phases) == 0 || phases[len(phases)-1] != pr.Phase {
			phases = append(phases, pr.Phase)
		}
		pcts = append(pcts, pr.Pct)
	}
	res, err := Run(context.Background(), &fakePinger{rtt: 12 * time.Millisecond}, tinyOptions(srv, true), progress)
	if err != nil {
		t.Fatal(err)
	}
	want := []Phase{PhaseIdle, PhaseDownload, PhaseUpload, PhaseDone}
	if len(phases) != len(want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phases = %v, want %v", phases, want)
		}
	}
	for i := 1; i < len(pcts); i++ {
		if pcts[i]+1e-9 < pcts[i-1] {
			t.Fatalf("progress went backwards at %d: %v -> %v", i, pcts[i-1], pcts[i])
		}
	}
	if pcts[len(pcts)-1] != 1 {
		t.Fatalf("final pct = %v, want 1", pcts[len(pcts)-1])
	}
	if res.DownMbps <= 0 || res.UpMbps <= 0 {
		t.Fatalf("throughput not measured: down %v up %v", res.DownMbps, res.UpMbps)
	}
	if res.DownPeakMbps < res.DownMbps || res.UpPeakMbps < res.UpMbps {
		t.Fatalf("peak below average: %+v", res)
	}
	if res.DownGrade == "" || res.UpGrade == "" {
		t.Fatalf("grades missing: %+v", res)
	}
	if res.IdleRTT != 12*time.Millisecond || res.DownRTT != 12*time.Millisecond {
		t.Fatalf("RTTs: idle %v down %v", res.IdleRTT, res.DownRTT)
	}
	if res.DownGrade != "A+" || res.UpGrade != "A+" {
		t.Fatalf("flat latency should grade A+, got %s / %s", res.DownGrade, res.UpGrade)
	}
	if res.Source != "local" || res.Colo != "EWR" {
		t.Fatalf("source/colo = %q/%q", res.Source, res.Colo)
	}
	if res.Duration <= 0 || res.IdleSamples != 3 || res.DownSamples == 0 || res.UpSamples == 0 {
		t.Fatalf("bookkeeping: %+v", res)
	}
	if s := Summary(res); s == "" || !strings.Contains(s, "Download") {
		t.Fatalf("summary = %q", s)
	}
}

func TestRunSkipsUploadWithoutURL(t *testing.T) {
	srv := newServer(t, true)
	res, err := Run(context.Background(), &fakePinger{rtt: 8 * time.Millisecond}, tinyOptions(srv, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.UpMbps != 0 || res.UpGrade != "" || res.UpSamples != 0 {
		t.Fatalf("upload should be skipped: %+v", res)
	}
	if !strings.Contains(Summary(res), "upload not measured") {
		t.Fatalf("summary should say upload not measured: %q", Summary(res))
	}
}

func TestRunFailsWhenNotSaturated(t *testing.T) {
	srv := newServer(t, false)
	_, err := Run(context.Background(), &fakePinger{rtt: 8 * time.Millisecond}, tinyOptions(srv, true), nil)
	if err == nil || !strings.Contains(err.Error(), "not saturated") {
		t.Fatalf("expected not-saturated error, got %v", err)
	}
}

func TestRunHonoursCancel(t *testing.T) {
	srv := newServer(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, &fakePinger{rtt: 8 * time.Millisecond}, tinyOptions(srv, true), nil); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}

func TestRateThroughput(t *testing.T) {
	cases := []struct {
		mbps float64
		want metrics.Rating
	}{
		{0, metrics.RatingUnknown},
		{3, metrics.RatingPoor},
		{5, metrics.RatingPlayable},
		{24.9, metrics.RatingPlayable},
		{25, metrics.RatingGood},
		{99, metrics.RatingGood},
		{100, metrics.RatingExcellent},
		{940, metrics.RatingExcellent},
	}
	for _, c := range cases {
		if got := RateThroughput(c.mbps); got != c.want {
			t.Errorf("RateThroughput(%v) = %v, want %v", c.mbps, got, c.want)
		}
	}
}

func TestSummaryVariants(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	good := Result{DownMbps: 412, UpMbps: 21, DownGrade: "A", DownAdded: ms(4), UpGrade: "A+", UpAdded: ms(2)}
	if s := Summary(good); !strings.Contains(s, "both directions") || !strings.Contains(s, "412 Mbps") {
		t.Errorf("good: %q", s)
	}
	upBad := Result{DownMbps: 412, UpMbps: 21, DownGrade: "A", DownAdded: ms(4), UpGrade: "C", UpAdded: ms(85)}
	if s := Summary(upBad); !strings.Contains(s, "climbs 85 ms while uploading") || !strings.Contains(s, "grade C") {
		t.Errorf("upBad: %q", s)
	}
	downBad := Result{DownMbps: 95.5, UpMbps: 21, DownGrade: "D", DownAdded: ms(150), UpGrade: "A", UpAdded: ms(3)}
	if s := Summary(downBad); !strings.Contains(s, "climbs 150 ms while downloading") || !strings.Contains(s, "95.5 Mbps") {
		t.Errorf("downBad: %q", s)
	}
	bothBad := Result{DownMbps: 50, UpMbps: 5, DownGrade: "C", DownAdded: ms(70), UpGrade: "F", UpAdded: ms(400)}
	if s := Summary(bothBad); !strings.Contains(s, "both directions") || !strings.Contains(s, "grade F") {
		t.Errorf("bothBad: %q", s)
	}
	noUp := Result{DownMbps: 50, DownGrade: "B", DownAdded: ms(30)}
	if s := Summary(noUp); !strings.Contains(s, "upload not measured") || !strings.Contains(s, "grade B") {
		t.Errorf("noUp: %q", s)
	}
	noUpBad := Result{DownMbps: 50, DownGrade: "D", DownAdded: ms(200)}
	if s := Summary(noUpBad); !strings.Contains(s, "SQM") {
		t.Errorf("noUpBad: %q", s)
	}
}
