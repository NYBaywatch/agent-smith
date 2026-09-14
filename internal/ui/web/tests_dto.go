package web

import (
	"fmt"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/dnsbench"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/speedtest"
	"github.com/NYBaywatch/agent-smith/internal/stability"
	"github.com/NYBaywatch/agent-smith/internal/store"
)

// TestsDTO carries the last result of every on-demand test.
type TestsDTO struct {
	Bufferbloat *BufferbloatResult `json:"bufferbloat"`
	Speed       *SpeedDTO          `json:"speed"`
	Stability   *StabilityDTO      `json:"stability"`
	DNS         *DNSBenchDTO       `json:"dns"`
}

// SpeedDTO is a speed-test result.
type SpeedDTO struct {
	When         string  `json:"when"`
	Source       string  `json:"source"`
	Colo         string  `json:"colo"`
	IdleMs       float64 `json:"idle_ms"`
	DownMbps     float64 `json:"down_mbps"`
	UpMbps       float64 `json:"up_mbps"`
	DownPeakMbps float64 `json:"down_peak_mbps"`
	UpPeakMbps   float64 `json:"up_peak_mbps"`
	DownRTTMs    float64 `json:"down_rtt_ms"`
	UpRTTMs      float64 `json:"up_rtt_ms"`
	DownAddedMs  float64 `json:"down_added_ms"`
	UpAddedMs    float64 `json:"up_added_ms"`
	DownGrade    string  `json:"down_grade"`
	UpGrade      string  `json:"up_grade"`
	DownRating   string  `json:"down_rating"`
	UpRating     string  `json:"up_rating"`
	UpMeasured   bool    `json:"up_measured"`
	DurationS    float64 `json:"duration_s"`
	Summary      string  `json:"summary"`
	Error        string  `json:"error,omitempty"`
}

// StabilityDTO is a probe-burst result.
type StabilityDTO struct {
	When      string    `json:"when"`
	Target    string    `json:"target"`
	Name      string    `json:"name"`
	Sent      int       `json:"sent"`
	Recv      int       `json:"recv"`
	LossPct   float64   `json:"loss_pct"`
	MeanMs    float64   `json:"mean_ms"`
	P50Ms     float64   `json:"p50_ms"`
	P95Ms     float64   `json:"p95_ms"`
	P99Ms     float64   `json:"p99_ms"`
	MaxMs     float64   `json:"max_ms"`
	JitterMs  float64   `json:"jitter_ms"`
	MaxGap    int       `json:"max_gap"`
	Spikes    int       `json:"spikes"`
	Samples   []float64 `json:"samples"`
	Rating    string    `json:"rating"`
	Verdict   string    `json:"verdict"`
	Detail    string    `json:"detail"`
	DurationS float64   `json:"duration_s"`
	Error     string    `json:"error,omitempty"`
}

// ResolverDTO is one resolver's benchmark line.
type ResolverDTO struct {
	Name       string  `json:"name"`
	Addr       string  `json:"addr"`
	Queries    int     `json:"queries"`
	Failed     int     `json:"failed"`
	MedianMs   float64 `json:"median_ms"`
	P95Ms      float64 `json:"p95_ms"`
	MaxMs      float64 `json:"max_ms"`
	UncachedMs float64 `json:"uncached_ms"`
	OK         bool    `json:"ok"`
	Rank       int     `json:"rank"`
	Rating     string  `json:"rating"`
}

// DNSBenchDTO is a resolver-benchmark result.
type DNSBenchDTO struct {
	When           string        `json:"when"`
	Fastest        string        `json:"fastest"`
	Current        string        `json:"current"`
	Recommendation string        `json:"recommendation"`
	Resolvers      []ResolverDTO `json:"resolvers"`
	DurationS      float64       `json:"duration_s"`
	Error          string        `json:"error,omitempty"`
}

// TestProgress is the live update pushed as the "test-progress" event.
type TestProgress struct {
	Test   string  `json:"test"` // bufferbloat | speed | stability | dns
	Phase  string  `json:"phase"`
	Pct    float64 `json:"pct"` // 0..1
	Mbps   float64 `json:"mbps"`
	RTTMs  float64 `json:"rtt_ms"`
	Detail string  `json:"detail"`
	Done   int     `json:"done"`
	Total  int     `json:"total"`
}

// BuildTests maps the persisted last results.
func BuildTests(t store.Tests) TestsDTO {
	return TestsDTO{
		Bufferbloat: BuildBufferbloat(t.Bufferbloat),
		Speed:       BuildSpeed(t.Speed),
		Stability:   BuildStability(t.Stability),
		DNS:         BuildDNSBench(t.DNSBench),
	}
}

// BuildBufferbloat maps a bufferbloat result (nil-safe).
func BuildBufferbloat(r *bufferbloat.Result) *BufferbloatResult {
	if r == nil {
		return nil
	}
	return &BufferbloatResult{
		Grade: r.Grade, AddedMs: ms(r.Added), IdleMs: ms(r.IdleRTT), LoadedMs: ms(r.LoadedRTT), DownMbps: r.DownloadMbps,
		Source: r.Source, Colo: r.Colo,
	}
}

// BuildSpeed maps a speed-test result (nil-safe).
func BuildSpeed(r *speedtest.Result) *SpeedDTO {
	if r == nil {
		return nil
	}
	d := &SpeedDTO{
		When: rfc(r.When), Source: r.Source, Colo: r.Colo,
		IdleMs: ms(r.IdleRTT), DownMbps: r.DownMbps, UpMbps: r.UpMbps,
		DownPeakMbps: r.DownPeakMbps, UpPeakMbps: r.UpPeakMbps,
		DownRTTMs: ms(r.DownRTT), UpRTTMs: ms(r.UpRTT),
		DownAddedMs: ms(r.DownAdded), UpAddedMs: ms(r.UpAdded),
		DownGrade: r.DownGrade, UpGrade: r.UpGrade,
		DownRating: ratingWord(speedtest.RateThroughput(r.DownMbps)),
		UpMeasured: r.UpBytes > 0,
		DurationS:  r.Duration.Seconds(),
		Summary:    speedtest.Summary(*r),
	}
	if d.UpMeasured {
		d.UpRating = ratingWord(speedtest.RateThroughput(r.UpMbps))
	} else {
		d.UpRating = "—"
	}
	return d
}

// BuildStability maps a burst result (nil-safe).
func BuildStability(r *stability.Result) *StabilityDTO {
	if r == nil {
		return nil
	}
	samples := r.Samples
	if samples == nil {
		samples = []float64{}
	}
	return &StabilityDTO{
		When: rfc(r.When), Target: r.Target, Name: r.Name,
		Sent: r.Sent, Recv: r.Recv, LossPct: r.Loss * 100,
		MeanMs: ms(r.Mean), P50Ms: ms(r.P50), P95Ms: ms(r.P95), P99Ms: ms(r.P99), MaxMs: ms(r.Max), JitterMs: ms(r.Jitter),
		MaxGap: r.MaxGap, Spikes: r.Spikes, Samples: samples,
		Rating: ratingWord(r.Rating), Verdict: r.Verdict, Detail: r.Detail,
		DurationS: r.Duration.Seconds(),
	}
}

// BuildDNSBench maps a resolver benchmark (nil-safe).
func BuildDNSBench(r *dnsbench.Result) *DNSBenchDTO {
	if r == nil {
		return nil
	}
	out := &DNSBenchDTO{When: rfc(r.When), Fastest: r.Fastest, Current: r.Current, Recommendation: r.Recommendation, DurationS: r.Duration.Seconds(), Resolvers: []ResolverDTO{}}
	for _, x := range r.Resolvers {
		out.Resolvers = append(out.Resolvers, ResolverDTO{
			Name: x.Name, Addr: x.Addr, Queries: x.Queries, Failed: x.Failed,
			MedianMs: ms(x.Median), P95Ms: ms(x.P95), MaxMs: ms(x.Max), UncachedMs: ms(x.Uncached),
			OK: x.OK, Rank: x.Rank, Rating: ratingWord(x.Rating),
		})
	}
	return out
}

// stabilityProgressDetail formats the burst progress line.
func stabilityProgressDetail(done, total int, rtt time.Duration, ok bool) string {
	if !ok {
		return fmt.Sprintf("probe %d of %d lost", done, total)
	}
	return fmt.Sprintf("probe %d of %d · %.1f ms", done, total, ms(rtt))
}

var _ = metrics.RatingUnknown // keep metrics imported for ratingWord callers
