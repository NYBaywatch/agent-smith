package synth

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
)

func TestRunPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Server", "unit")
		w.Header().Set("Cf-Ray", "abc123-EWR")
		// A loopback round trip can finish inside one clock tick on Windows;
		// hold the response briefly so TTFB and Total are measurably > 0.
		time.Sleep(15 * time.Millisecond)
		_, _ = w.Write([]byte(strings.Repeat("x", 1234)))
	}))
	defer srv.Close()

	r := Run(context.Background(), Check{Name: "t", URL: srv.URL}, 5*time.Second)
	if !r.OK {
		t.Fatalf("not OK: %+v", r)
	}
	if r.Status != 200 {
		t.Errorf("Status = %d", r.Status)
	}
	if r.Bytes != 1234 {
		t.Errorf("Bytes = %d", r.Bytes)
	}
	if r.Timing.Total <= 0 || r.Timing.TTFB <= 0 {
		t.Errorf("timing not populated: %+v", r.Timing)
	}
	// Loopback connects complete within Windows' ~0.5 ms clock resolution,
	// so Connect can legitimately round to 0; only guard against negatives.
	if r.Timing.Connect < 0 {
		t.Errorf("Connect = %v", r.Timing.Connect)
	}
	if r.Timing.TLS != 0 {
		t.Errorf("TLS should be 0 for plain http, got %v", r.Timing.TLS)
	}
	if r.RemoteIP != "127.0.0.1" {
		t.Errorf("RemoteIP = %q", r.RemoteIP)
	}
	if r.Edge != "Cloudflare EWR" {
		t.Errorf("Edge = %q", r.Edge)
	}
	if r.Server != "unit" {
		t.Errorf("Server = %q", r.Server)
	}
	if r.When.IsZero() {
		t.Error("When is zero")
	}
}

func TestRunTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	testTLSConfig = &tls.Config{InsecureSkipVerify: true}
	defer func() { testTLSConfig = nil }()

	r := Run(context.Background(), Check{Name: "tls", URL: srv.URL}, 5*time.Second)
	if !r.OK {
		t.Fatalf("not OK: %+v", r)
	}
	if r.Timing.TLS <= 0 {
		t.Errorf("TLS = %v, want > 0", r.Timing.TLS)
	}
	if !strings.HasPrefix(r.Proto, "HTTP/") {
		t.Errorf("Proto = %q", r.Proto)
	}
}

func TestRunStatusRules(t *testing.T) {
	var code int32 = 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(atomic.LoadInt32(&code)))
	}))
	defer srv.Close()

	// 4xx counts as reachable when ExpectStatus is 0.
	atomic.StoreInt32(&code, 403)
	if r := Run(context.Background(), Check{URL: srv.URL}, 5*time.Second); !r.OK || r.Status != 403 {
		t.Errorf("403 with ExpectStatus=0: OK=%v status=%d err=%q", r.OK, r.Status, r.Err)
	}
	// 5xx is a failure.
	atomic.StoreInt32(&code, 500)
	if r := Run(context.Background(), Check{URL: srv.URL}, 5*time.Second); r.OK || r.Status != 500 || r.Err == "" {
		t.Errorf("500: OK=%v status=%d err=%q", r.OK, r.Status, r.Err)
	}
	// ExpectStatus mismatch is a failure, even for 200.
	atomic.StoreInt32(&code, 200)
	if r := Run(context.Background(), Check{URL: srv.URL, ExpectStatus: 204}, 5*time.Second); r.OK {
		t.Errorf("expect 204 got 200: OK should be false (err=%q)", r.Err)
	}
	// ExpectStatus match.
	atomic.StoreInt32(&code, 204)
	if r := Run(context.Background(), Check{URL: srv.URL, ExpectStatus: 204}, 5*time.Second); !r.OK {
		t.Errorf("expect 204 got 204: OK should be true (err=%q)", r.Err)
	}
}

func TestRunTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	r := Run(context.Background(), Check{URL: srv.URL}, 150*time.Millisecond)
	if r.OK {
		t.Fatal("expected failure on timeout")
	}
	if r.Err == "" {
		t.Error("Err should be set")
	}
	if r.Timing.Total < 100*time.Millisecond {
		t.Errorf("Total = %v, expected to have waited for the timeout", r.Timing.Total)
	}
}

func TestRunBadURL(t *testing.T) {
	r := Run(context.Background(), Check{URL: "://nope"}, time.Second)
	if r.OK || r.Err == "" {
		t.Errorf("bad URL: OK=%v err=%q", r.OK, r.Err)
	}
}

func TestRunBodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("y", 64<<10))
		for i := 0; i < 48; i++ { // 3 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	r := Run(context.Background(), Check{URL: srv.URL}, 5*time.Second)
	if !r.OK {
		t.Fatalf("not OK: %+v", r)
	}
	if r.Bytes != maxBody {
		t.Errorf("Bytes = %d, want cap %d", r.Bytes, maxBody)
	}
}

func TestEdgeFromHeaders(t *testing.T) {
	mk := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name          string
		h             http.Header
		provider, pop string
	}{
		{"cloudflare", mk("Cf-Ray", "8a1b2c3d4e5f6789-EWR"), "Cloudflare", "EWR"},
		{"cloudflare server only", mk("Server", "cloudflare"), "Cloudflare", ""},
		{"fastly", mk("X-Served-By", "cache-ewr18123-EWR"), "Fastly", "EWR"},
		{"fastly multi", mk("X-Served-By", "cache-lga1234-LGA, cache-ewr18123-EWR"), "Fastly", "EWR"},
		{"cloudfront", mk("X-Amz-Cf-Pop", "EWR52-C1"), "CloudFront", "EWR52-C1"},
		{"cloudfront via", mk("Via", "1.1 abc.cloudfront.net (CloudFront)"), "CloudFront", ""},
		{"akamai server", mk("Server", "AkamaiGHost"), "Akamai", ""},
		{"akamai header", mk("X-Akamai-Request-Id", "abc.def"), "Akamai", "abc.def"},
		{"akamai xcache", mk("X-Cache", "TCP_HIT from a1-2-3.deploy.akamaitechnologies.com"), "Akamai", "TCP_HIT from a1-2-3.deploy.akamaitechnologies.com"},
		{"google gws", mk("Server", "gws"), "Google", ""},
		{"google sffe", mk("Server", "sffe"), "Google", ""},
		{"google esf", mk("Server", "ESF"), "Google", ""},
		{"none", mk("Server", "nginx"), "", ""},
		{"empty", mk(), "", ""},
	}
	for _, c := range cases {
		p, pop := EdgeFromHeaders(c.h)
		if p != c.provider || pop != c.pop {
			t.Errorf("%s: got (%q,%q) want (%q,%q)", c.name, p, pop, c.provider, c.pop)
		}
	}
}

func TestMonitorStats(t *testing.T) {
	c := Check{Name: "a", URL: "http://a"}
	m := NewMonitor([]Check{c}, 5)

	// No results yet → zero stats.
	if s := m.Summaries(); len(s) != 1 || s[0].Stats.Runs != 0 || !s[0].Stats.Last.When.IsZero() {
		t.Fatalf("empty summaries = %+v", s)
	}

	add := func(ok bool, total time.Duration) {
		m.Add(Result{Check: c, When: time.Now(), OK: ok, Timing: Timing{Total: total, TTFB: total / 2}})
	}
	// 7 adds into a window of 5: the first two roll out.
	add(false, 0)
	add(false, 0)
	add(true, 100*time.Millisecond)
	add(true, 200*time.Millisecond)
	add(true, 300*time.Millisecond)
	add(false, 0)
	add(false, 0)

	st := m.Summaries()[0].Stats
	if st.Runs != 5 || st.Fails != 2 {
		t.Errorf("Runs=%d Fails=%d", st.Runs, st.Fails)
	}
	if st.Availability < 0.59 || st.Availability > 0.61 {
		t.Errorf("Availability = %v", st.Availability)
	}
	if st.Consecutive != 2 || !st.Down() {
		t.Errorf("Consecutive = %d Down=%v", st.Consecutive, st.Down())
	}
	if st.Mean != 200*time.Millisecond {
		t.Errorf("Mean = %v", st.Mean)
	}
	if st.P50 != 200*time.Millisecond {
		t.Errorf("P50 = %v", st.P50)
	}
	if st.P95 != 300*time.Millisecond || st.Max != 300*time.Millisecond {
		t.Errorf("P95 = %v Max = %v", st.P95, st.Max)
	}
	if st.MeanTTFB != 100*time.Millisecond {
		t.Errorf("MeanTTFB = %v", st.MeanTTFB)
	}
	if st.Last.OK {
		t.Error("Last should be the newest (failed) result")
	}

	// A success resets Consecutive.
	add(true, 50*time.Millisecond)
	if st := m.Summaries()[0].Stats; st.Consecutive != 0 || st.Down() {
		t.Errorf("after success: Consecutive=%d", st.Consecutive)
	}

	// SetChecks keeps the surviving window and drops the removed one.
	b := Check{Name: "b", URL: "http://b"}
	m.Add(Result{Check: b, OK: true, Timing: Timing{Total: time.Millisecond}})
	m.SetChecks([]Check{b})
	if s := m.Summaries(); len(s) != 1 || s[0].Check.URL != "http://b" || s[0].Stats.Runs != 1 {
		t.Errorf("after SetChecks: %+v", s)
	}
	m.SetChecks([]Check{c})
	if s := m.Summaries(); s[0].Stats.Runs != 0 {
		t.Errorf("window for removed URL should be gone, got Runs=%d", s[0].Stats.Runs)
	}
	if ch := m.Checks(); len(ch) != 1 || ch[0].Name != "a" {
		t.Errorf("Checks() = %+v", ch)
	}
}

func TestStatsSlow(t *testing.T) {
	if (Stats{MeanTTFB: 900 * time.Millisecond}).Slow() != true {
		t.Error("TTFB 900ms should be slow")
	}
	if (Stats{Mean: 3 * time.Second}).Slow() != true {
		t.Error("mean 3s should be slow")
	}
	if (Stats{Mean: 500 * time.Millisecond, MeanTTFB: 100 * time.Millisecond}).Slow() {
		t.Error("fast should not be slow")
	}
}

func TestPresets(t *testing.T) {
	ps := Presets()
	if len(ps) == 0 {
		t.Fatal("no presets")
	}
	valid := map[Category]bool{CategorySaaS: true, CategoryCloud: true, CategoryCDN: true, CategoryAI: true}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name == "" {
			t.Errorf("preset with empty name: %+v", p)
		}
		if !strings.HasPrefix(p.URL, "https://") {
			t.Errorf("%s: URL %q not https", p.Name, p.URL)
		}
		if seen[p.URL] {
			t.Errorf("duplicate URL %q", p.URL)
		}
		seen[p.URL] = true
		if !valid[p.Category] {
			t.Errorf("%s: invalid category %q", p.Name, p.Category)
		}
		if !p.IsEnabled() {
			t.Errorf("%s: preset should be enabled", p.Name)
		}
	}
	// Copy semantics.
	ps[0].Name = "mutated"
	if Presets()[0].Name == "mutated" {
		t.Error("Presets() returned shared slice")
	}
	for _, cat := range []Category{CategorySaaS, CategoryCloud, CategoryCDN, CategoryAI} {
		sub := PresetsByCategory(cat)
		if len(sub) == 0 {
			t.Errorf("no presets in %s", cat)
		}
		for _, p := range sub {
			if p.Category != cat {
				t.Errorf("PresetsByCategory(%s) returned %s", cat, p.Category)
			}
		}
	}
	if len(PresetsByCategory(CategoryCustom)) != 0 {
		t.Error("no built-in custom presets expected")
	}
}

func TestIsEnabled(t *testing.T) {
	f := false
	tr := true
	if !(Check{}).IsEnabled() || !(Check{Enabled: &tr}).IsEnabled() || (Check{Enabled: &f}).IsEnabled() {
		t.Error("IsEnabled semantics wrong")
	}
}

func TestRunAll(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	off := false
	checks := []Check{
		{Name: "1", URL: srv.URL + "/1"},
		{Name: "2", URL: srv.URL + "/2"},
		{Name: "3", URL: srv.URL + "/3"},
		{Name: "4", URL: srv.URL + "/4"},
		{Name: "5", URL: srv.URL + "/5"},
		{Name: "off", URL: srv.URL + "/off", Enabled: &off},
	}
	m := NewMonitor(checks, 0)
	m.RunAll(context.Background(), 5*time.Second, 3)

	if got := atomic.LoadInt32(&hits); got != 5 {
		t.Errorf("server hits = %d, want 5 (disabled check must be skipped)", got)
	}
	sums := m.Summaries()
	if len(sums) != 6 {
		t.Fatalf("summaries = %d", len(sums))
	}
	for _, s := range sums[:5] {
		if s.Stats.Runs != 1 || !s.Stats.Last.OK {
			t.Errorf("%s: Runs=%d OK=%v err=%q", s.Check.Name, s.Stats.Runs, s.Stats.Last.OK, s.Stats.Last.Err)
		}
	}
	if sums[5].Stats.Runs != 0 {
		t.Errorf("disabled check ran: %+v", sums[5].Stats)
	}
}

func TestRatings(t *testing.T) {
	if RateTTFB(0) != metrics.RatingUnknown || RateTotal(0) != metrics.RatingUnknown {
		t.Error("zero should be unknown")
	}
	if RateTTFB(100*time.Millisecond) != metrics.RatingExcellent || RateTTFB(300*time.Millisecond) != metrics.RatingGood ||
		RateTTFB(700*time.Millisecond) != metrics.RatingPlayable || RateTTFB(1500*time.Millisecond) != metrics.RatingPoor {
		t.Error("RateTTFB buckets wrong")
	}
	if RateTotal(400*time.Millisecond) != metrics.RatingExcellent || RateTotal(800*time.Millisecond) != metrics.RatingGood ||
		RateTotal(2*time.Second) != metrics.RatingPlayable || RateTotal(3*time.Second) != metrics.RatingPoor {
		t.Error("RateTotal buckets wrong")
	}
}

func TestPercentile(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if p := percentile(s, 50); p != 5 {
		t.Errorf("p50 = %v", p)
	}
	if p := percentile(s, 95); p != 10 {
		t.Errorf("p95 = %v", p)
	}
	if p := percentile(s, 0); p != 1 {
		t.Errorf("p0 = %v", p)
	}
	if p := percentile([]float64{7}, 95); p != 7 {
		t.Errorf("single = %v", p)
	}
}
