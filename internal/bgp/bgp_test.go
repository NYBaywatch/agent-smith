package bgp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sample is a trimmed-but-realistic RIPEstat routing-status document.
const sample = `{
  "messages": [],
  "see_also": [],
  "version": "3.3",
  "data_call_name": "routing-status",
  "data_call_status": "supported",
  "cached": false,
  "data": {
    "first_seen": {"prefix": "67.80.0.0/13", "origin": "6128", "time": "2003-05-16T08:00:00"},
    "last_seen":  {"prefix": "67.80.0.0/13", "origin": "6128", "time": "2026-09-14T07:59:00"},
    "visibility": {
      "v4": {"ris_peers_seeing": 340, "total_ris_peers": 345},
      "v6": {"ris_peers_seeing": 0,   "total_ris_peers": 330}
    },
    "announced_space": {"v4": {"prefixes": 1, "ips": 524288}, "v6": {"prefixes": 0, "48s": 0}},
    "observed_neighbours": 14,
    "resource": "67.83.1.2",
    "query_time": "2026-09-14T08:00:00"
  },
  "query_id": "20260914080000-abc",
  "process_time": 12,
  "server_id": "app1",
  "build_version": "live.2026.9.1",
  "status": "ok",
  "status_code": 200,
  "time": "2026-09-14T08:00:00.123456"
}`

const unannounced = `{"data": {
  "first_seen": {}, "last_seen": {},
  "visibility": {"v4": {"ris_peers_seeing": 0, "total_ris_peers": 345}, "v6": {"ris_peers_seeing": 0, "total_ris_peers": 330}},
  "resource": "192.0.2.1"
}, "status": "ok"}`

func withServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	old := baseURL
	baseURL = srv.URL + "/data.json"
	t.Cleanup(func() { baseURL = old; srv.Close() })
}

func TestFetchParsesRoutingStatus(t *testing.T) {
	var gotResource, gotUA string
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotResource = r.URL.Query().Get("resource")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sample))
	})
	st, err := Fetch(context.Background(), "67.83.1.2")
	if err != nil {
		t.Fatal(err)
	}
	if gotResource != "67.83.1.2" {
		t.Errorf("resource query %q", gotResource)
	}
	if gotUA != userAgent {
		t.Errorf("user agent %q", gotUA)
	}
	if st.Resource != "67.83.1.2" || st.Prefix != "67.80.0.0/13" || st.OriginASN != 6128 {
		t.Errorf("identity: %+v", st)
	}
	if st.VisiblePeers != 340 || st.TotalPeers != 345 || st.Visibility < 0.98 || st.Visibility > 0.99 {
		t.Errorf("visibility: %+v", st)
	}
	if !st.Announced || !st.Healthy() {
		t.Errorf("expected announced+healthy: %+v", st)
	}
	want := time.Date(2003, 5, 16, 8, 0, 0, 0, time.UTC)
	if !st.FirstSeen.Equal(want) {
		t.Errorf("first seen %v want %v", st.FirstSeen, want)
	}
	if st.FetchedAt.IsZero() {
		t.Error("FetchedAt not set")
	}
}

func TestFetchUnannounced(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(unannounced)) })
	st, err := Fetch(context.Background(), "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Announced || st.Healthy() || st.Prefix != "" || st.OriginASN != 0 || st.Visibility != 0 {
		t.Errorf("unexpected %+v", st)
	}
	if !st.FirstSeen.IsZero() {
		t.Errorf("first seen should be zero: %v", st.FirstSeen)
	}
}

func TestFetchV6UsesV6Visibility(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"last_seen":{"prefix":"2001:db8::/32","origin":13335},
		  "visibility":{"v4":{"ris_peers_seeing":0,"total_ris_peers":345},"v6":{"ris_peers_seeing":300,"total_ris_peers":330}}},"status":"ok"}`))
	})
	st, err := Fetch(context.Background(), "2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	if st.OriginASN != 13335 || st.VisiblePeers != 300 || st.TotalPeers != 330 || !st.Healthy() {
		t.Errorf("v6: %+v", st)
	}
}

func TestFetchErrors(t *testing.T) {
	if _, err := Fetch(context.Background(), "  "); err == nil {
		t.Error("expected error for empty resource")
	}
	withServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := Fetch(context.Background(), "1.1.1.1"); err == nil {
		t.Error("expected error on HTTP 500")
	}
	withServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) })
	if _, err := Fetch(context.Background(), "1.1.1.1"); err == nil {
		t.Error("expected decode error")
	}
	withServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"error","data":{}}`)) })
	if _, err := Fetch(context.Background(), "1.1.1.1"); err == nil {
		t.Error("expected error on RIPEstat status=error")
	}
	var nilStatus *Status
	if nilStatus.Healthy() {
		t.Error("nil status must not be healthy")
	}
}
