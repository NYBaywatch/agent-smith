// Package bgp checks how the wider internet sees this connection's public
// address: whether the covering prefix is announced in BGP, by which origin
// AS, and how many of RIPE RIS's route collectors can see it. A prefix that
// drops out of the global routing table — or becomes visible from only a
// fraction of collectors — is unreachable from parts of the internet even
// though the local link looks perfectly healthy.
//
// Data comes from the public RIPEstat "routing-status" API (no key required).
// It is a best-effort, low-frequency lookup: callers should treat errors as
// "unknown", never as "down".
package bgp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Status summarises global BGP visibility of the prefix covering an address.
type Status struct {
	Resource     string    `json:"resource"`      // the IP queried
	Prefix       string    `json:"prefix"`        // announced prefix covering it, e.g. "67.80.0.0/13"
	OriginASN    int       `json:"origin_asn"`    // AS currently originating the prefix
	Visibility   float64   `json:"visibility"`    // 0..1 = RIS peers seeing the prefix / total peers
	VisiblePeers int       `json:"visible_peers"` // RIS peers that see the prefix
	TotalPeers   int       `json:"total_peers"`   // RIS peers for that address family
	Announced    bool      `json:"announced"`     // a covering prefix is in the routing table
	FirstSeen    time.Time `json:"first_seen"`    // when RIS first saw the prefix (zero if unknown)
	FetchedAt    time.Time `json:"fetched_at"`
}

// Healthy reports whether the prefix is announced and seen by at least 90% of
// route collectors — i.e. reachable from essentially the whole internet.
func (s *Status) Healthy() bool {
	return s != nil && s.Announced && s.Visibility >= 0.9
}

const userAgent = "AgentSmith/1.0 (+https://github.com/NYBaywatch/agent-smith)"

// baseURL is the RIPEstat endpoint; tests point it at a local server.
var baseURL = "https://stat.ripe.net/data/routing-status/data.json"

// httpClient is shared so connections are reused across hourly refreshes.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// ripeResponse mirrors the subset of the routing-status document we consume.
type ripeResponse struct {
	Status   string   `json:"status"`
	Messages [][]any  `json:"messages"`
	Data     ripeData `json:"data"`
}

type ripeData struct {
	Resource   string   `json:"resource"`
	FirstSeen  ripeSeen `json:"first_seen"`
	LastSeen   ripeSeen `json:"last_seen"`
	Visibility struct {
		V4 ripeVis `json:"v4"`
		V6 ripeVis `json:"v6"`
	} `json:"visibility"`
}

type ripeSeen struct {
	Prefix string   `json:"prefix"`
	Origin flexASN  `json:"origin"`
	Time   ripeTime `json:"time"`
}

type ripeVis struct {
	Seeing int `json:"ris_peers_seeing"`
	Total  int `json:"total_ris_peers"`
}

// flexASN accepts the origin AS as either a JSON string ("6128") or a number.
type flexASN int

func (a *flexASN) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*a = 0
		return nil
	}
	s = strings.TrimPrefix(strings.ToUpper(s), "AS")
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("bgp: bad origin %q", s)
	}
	*a = flexASN(n)
	return nil
}

// ripeTime parses RIPEstat's "2006-01-02T15:04:05" timestamps (UTC, no zone),
// tolerating RFC 3339 as well. Unparseable values become the zero time.
type ripeTime time.Time

func (t *ripeTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*t = ripeTime(time.Time{})
		return nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", time.RFC3339} {
		if v, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			*t = ripeTime(v)
			return nil
		}
	}
	*t = ripeTime(time.Time{})
	return nil
}

// Fetch looks up the BGP routing status of the prefix covering ip.
func Fetch(ctx context.Context, ip string) (*Status, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, errors.New("bgp: empty resource")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"?resource="+url.QueryEscape(ip), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bgp: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bgp: RIPEstat returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("bgp: %w", err)
	}
	var rr ripeResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, fmt.Errorf("bgp: decode: %w", err)
	}
	if rr.Status != "" && rr.Status != "ok" {
		return nil, fmt.Errorf("bgp: RIPEstat status %q", rr.Status)
	}

	st := &Status{
		Resource:  ip,
		Prefix:    rr.Data.LastSeen.Prefix,
		OriginASN: int(rr.Data.LastSeen.Origin),
		FirstSeen: time.Time(rr.Data.FirstSeen.Time),
		FetchedAt: time.Now(),
	}
	if st.Resource == "" {
		st.Resource = rr.Data.Resource
	}
	vis := rr.Data.Visibility.V4
	if strings.Contains(ip, ":") {
		vis = rr.Data.Visibility.V6
	}
	st.VisiblePeers, st.TotalPeers = vis.Seeing, vis.Total
	if vis.Total > 0 {
		st.Visibility = float64(vis.Seeing) / float64(vis.Total)
	}
	st.Announced = st.Prefix != ""
	return st, nil
}
