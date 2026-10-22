package stability

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// fakePinger answers probe i with script[i] (rtt 0 = lost).
type fakePinger struct {
	mu     sync.Mutex
	script func(i int) (time.Duration, bool)
	calls  int
}

func (f *fakePinger) Ping(ctx context.Context, ip net.IP, timeout time.Duration) (probe.Result, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.mu.Unlock()
	rtt, ok := f.script(i)
	if !ok {
		return probe.Result{Status: "timeout"}, nil
	}
	return probe.Result{Addr: ip, RTT: rtt, OK: true, Status: "ok"}, nil
}
func (f *fakePinger) PingTTL(ctx context.Context, ip net.IP, ttl int, timeout time.Duration) (probe.Result, error) {
	return probe.Result{}, errors.New("unused")
}
func (f *fakePinger) Close() error { return nil }

func opts(count int) Options {
	o := DefaultOptions(net.IPv4(1, 1, 1, 1), "Cloudflare")
	o.Count = count
	o.Interval = time.Millisecond
	return o
}

func TestCleanBurstIsExcellent(t *testing.T) {
	p := &fakePinger{script: func(i int) (time.Duration, bool) {
		return 18*time.Millisecond + time.Duration(i%3)*time.Millisecond, true
	}}
	var progress int
	var mu sync.Mutex
	o := opts(50)
	o.Progress = func(done, total int, rtt time.Duration, ok bool) { mu.Lock(); progress++; mu.Unlock() }
	r, err := Run(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sent != 50 || r.Recv != 50 || r.Loss != 0 || r.MaxGap != 0 {
		t.Fatalf("sent %d recv %d loss %v gap %d", r.Sent, r.Recv, r.Loss, r.MaxGap)
	}
	if r.Rating != metrics.RatingExcellent {
		t.Errorf("rating %v (%s / %s)", r.Rating, r.Verdict, r.Detail)
	}
	if len(r.Samples) != 50 || r.Samples[0] <= 0 {
		t.Errorf("samples %d first %v", len(r.Samples), r.Samples[0])
	}
	if progress != 50 {
		t.Errorf("progress calls %d", progress)
	}
	if r.Target != "1.1.1.1" || r.Name != "Cloudflare" || r.Duration <= 0 {
		t.Errorf("meta %+v", r)
	}
}

func TestOutageIsPoorWithGap(t *testing.T) {
	p := &fakePinger{script: func(i int) (time.Duration, bool) {
		if i >= 10 && i < 14 {
			return 0, false
		}
		return 20 * time.Millisecond, true
	}}
	r, err := Run(context.Background(), p, opts(40))
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxGap != 4 {
		t.Errorf("MaxGap %d want 4", r.MaxGap)
	}
	if r.Rating != metrics.RatingPoor {
		t.Errorf("rating %v want Poor (%s)", r.Rating, r.Detail)
	}
	for i := 10; i < 14; i++ {
		if r.Samples[i] != 0 {
			t.Errorf("sample %d = %v, want 0 for a lost probe", i, r.Samples[i])
		}
	}
	if r.Sent-r.Recv != 4 {
		t.Errorf("lost %d", r.Sent-r.Recv)
	}
}

func TestJitteryBurstHasSpikes(t *testing.T) {
	p := &fakePinger{script: func(i int) (time.Duration, bool) {
		if i%5 == 0 {
			return 120 * time.Millisecond, true
		}
		return 15 * time.Millisecond, true
	}}
	r, err := Run(context.Background(), p, opts(50))
	if err != nil {
		t.Fatal(err)
	}
	if r.Spikes == 0 {
		t.Errorf("expected spikes, got 0 (p50 %v max %v)", r.P50, r.Max)
	}
	if r.Rating == metrics.RatingExcellent {
		t.Errorf("jittery burst rated Excellent (jitter %v p99 %v)", r.Jitter, r.P99)
	}
}

func TestCancelReturnsPartial(t *testing.T) {
	p := &fakePinger{script: func(i int) (time.Duration, bool) { return 10 * time.Millisecond, true }}
	ctx, cancel := context.WithCancel(context.Background())
	o := opts(1000)
	o.Interval = 5 * time.Millisecond
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	r, err := Run(ctx, p, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if r.Sent == 0 || r.Sent >= 1000 {
		t.Errorf("partial sent %d", r.Sent)
	}
	if len(r.Samples) != r.Sent {
		t.Errorf("samples %d vs sent %d", len(r.Samples), r.Sent)
	}
}

func TestGradeEdges(t *testing.T) {
	if rt, v, _ := Grade(Result{}); rt != metrics.RatingUnknown || v == "" {
		t.Errorf("empty: %v %q", rt, v)
	}
	if rt, _, d := Grade(Result{Sent: 10}); rt != metrics.RatingPoor || d == "" {
		t.Errorf("unreachable: %v %q", rt, d)
	}
	if _, _, d := Grade(Result{Sent: 100, Recv: 100, Jitter: 22 * time.Millisecond, P50: 20 * time.Millisecond, P99: 90 * time.Millisecond}); d != "jitter 22 ms" {
		t.Errorf("detail %q", d)
	}
}

func TestNoTarget(t *testing.T) {
	if _, err := Run(context.Background(), &fakePinger{script: func(int) (time.Duration, bool) { return 0, false }}, Options{}); err == nil {
		t.Error("expected error for nil target")
	}
}
