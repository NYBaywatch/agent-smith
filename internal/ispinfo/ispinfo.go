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
	IP         string    `json:"ip"`
	ISP        string    `json:"isp"`
	Org        string    `json:"org"`
	AS         string    `json:"as"`      // e.g. "AS6128 Cablevision Systems Corp."
	ASName     string    `json:"as_name"` // e.g. "CABLE-NET-1"
	City       string    `json:"city"`
	Region     string    `json:"region"`
	Country    string    `json:"country"`
	Reverse    string    `json:"reverse"`
	ConnType   string    `json:"conn_type"` // Fixed line | Mobile | Hosting/VPN
	Support    string    `json:"support"`   // ISP repair/support phone (best-effort)
	SupportURL string    `json:"support_url"`
	FetchedAt  time.Time `json:"fetched_at"`
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

// support pairs an ISP repair/support phone with its official support/outage URL.
type support struct{ phone, url string }

// supportByASN maps an autonomous-system number to its ISP's support contact.
// Best-effort and US-focused; the URL is the authoritative fallback if a number
// is ever stale. Optimum (AS6128) is verified; extend as needed.
var supportByASN = map[string]support{
	"AS6128":  {"1-866-347-4784", "https://www.optimum.net/support/outage/"},                        // Optimum / Altice
	"AS7922":  {"1-800-934-6489", "https://www.xfinity.com/support/status"},                         // Comcast Xfinity
	"AS7018":  {"1-800-288-2020", "https://www.att.com/outages/"},                                   // AT&T
	"AS701":   {"1-800-837-4966", "https://www.verizon.com/support/residential/"},                   // Verizon
	"AS22773": {"1-800-234-3993", "https://www.cox.com/residential/support/check-for-outages.html"}, // Cox
	"AS20115": {"1-833-267-6094", "https://www.spectrum.net/support/internet"},                      // Charter Spectrum
	"AS209":   {"1-800-244-1111", "https://www.centurylink.com/home/help/"},                         // CenturyLink / Lumen
	"AS5650":  {"1-800-921-8101", "https://www.frontier.com/helpcenter"},                            // Frontier
	"AS21928": {"1-844-275-9310", "https://www.t-mobile.com/support/home-internet"},                 // T-Mobile Home Internet
}

// supportByName is a substring fallback (matched against ISP + org, lowercased).
var supportByName = []struct {
	match string
	s     support
}{
	{"optimum", support{"1-866-347-4784", "https://www.optimum.net/support/outage/"}},
	{"cablevision", support{"1-866-347-4784", "https://www.optimum.net/support/outage/"}},
	{"altice", support{"1-866-347-4784", "https://www.optimum.net/support/outage/"}},
	{"comcast", support{"1-800-934-6489", "https://www.xfinity.com/support/status"}},
	{"xfinity", support{"1-800-934-6489", "https://www.xfinity.com/support/status"}},
	{"spectrum", support{"1-833-267-6094", "https://www.spectrum.net/support/internet"}},
	{"charter", support{"1-833-267-6094", "https://www.spectrum.net/support/internet"}},
	{"at&t", support{"1-800-288-2020", "https://www.att.com/outages/"}},
	{"verizon", support{"1-800-837-4966", "https://www.verizon.com/support/residential/"}},
	{"cox", support{"1-800-234-3993", "https://www.cox.com/residential/support/check-for-outages.html"}},
	{"centurylink", support{"1-800-244-1111", "https://www.centurylink.com/home/help/"}},
	{"lumen", support{"1-800-244-1111", "https://www.centurylink.com/home/help/"}},
	{"frontier", support{"1-800-921-8101", "https://www.frontier.com/helpcenter"}},
	{"t-mobile", support{"1-844-275-9310", "https://www.t-mobile.com/support/home-internet"}},
	{"google fiber", support{"1-866-777-7550", "https://fiber.google.com/support/"}},
}

// lookupSupport finds the ISP support contact by ASN, then by ISP/org name.
func lookupSupport(i *Info) support {
	if s, ok := supportByASN[i.ASN()]; ok {
		return s
	}
	hay := strings.ToLower(i.ISP + " " + i.Org)
	for _, e := range supportByName {
		if strings.Contains(hay, e.match) {
			return e.s
		}
	}
	return support{}
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
	info := &Info{
		IP: raw.Query, ISP: raw.Isp, Org: raw.Org, AS: raw.As, ASName: raw.Asname,
		City: raw.City, Region: raw.RegionName, Country: raw.Country, Reverse: raw.Reverse,
		ConnType: ct, FetchedAt: time.Now(),
	}
	sup := lookupSupport(info)
	info.Support, info.SupportURL = sup.phone, sup.url
	return info, nil
}
