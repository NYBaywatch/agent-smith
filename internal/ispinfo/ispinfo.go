// Package ispinfo looks up the connection's public IP and the ISP / network
// (ASN) behind it, via a public geo-IP API. It's best-effort: on any error it
// returns nil so the rest of the app degrades gracefully.
package ispinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Info describes the connection's public identity.
type Info struct {
	IP        string    `json:"ip"`
	ISP       string    `json:"isp"`
	Org       string    `json:"org"`
	AS        string    `json:"as"`      // e.g. "AS6128 Cablevision Systems Corp."
	ASName    string    `json:"as_name"` // e.g. "CABLE-NET-1"
	City      string    `json:"city"`
	Region    string    `json:"region"`
	Country   string    `json:"country"`
	Reverse   string    `json:"reverse"`
	ConnType  string    `json:"conn_type"` // Fixed line | Mobile | Hosting/VPN
	FetchedAt time.Time `json:"fetched_at"`
}

// ASN returns just the AS number token (e.g. "AS6128") from AS.
func (i *Info) ASN() string {
	if f := strings.Fields(i.AS); len(f) > 0 {
		return f[0]
	}
	return ""
}

// Location returns a compact "City, Region, Country" string.
func (i *Info) Location() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{i.City, i.Region, i.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

const endpoint = "http://ip-api.com/json/?fields=status,message,query,isp,org,as,asname,city,regionName,country,reverse,mobile,hosting,proxy"

// Fetch queries the geo-IP API for the current public connection info.
func Fetch(ctx context.Context) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AgentSmith/1.0 (+https://github.com/NYBaywatch/agent-smith)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw struct {
		Status, Message, Query, Isp, Org, As, Asname string
		City, RegionName, Country, Reverse           string
		Mobile, Hosting, Proxy                       bool
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if raw.Status != "success" {
		msg := raw.Message
		if msg == "" {
			msg = "lookup failed"
		}
		return nil, fmt.Errorf("ispinfo: %s", msg)
	}

	ct := "Fixed line"
	switch {
	case raw.Mobile:
		ct = "Mobile"
	case raw.Hosting || raw.Proxy:
		ct = "Hosting / VPN"
	}
	return &Info{
		IP: raw.Query, ISP: raw.Isp, Org: raw.Org, AS: raw.As, ASName: raw.Asname,
		City: raw.City, Region: raw.RegionName, Country: raw.Country, Reverse: raw.Reverse,
		ConnType: ct, FetchedAt: time.Now(),
	}, nil
}
