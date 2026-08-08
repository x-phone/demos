// trunkbench — frame-timing measurement over a REAL SIP trunk.
// Companion to the loopback experiment: same metric (PCMReader inter-arrival),
// real network. Dial something that produces continuous audio (echo test,
// voicemail greeting, IVR).
//
// Usage:
//   SIP_USER=... SIP_PASS=... SIP_HOST=sip.telnyx.com TARGET=+1415XXXXXXX go run .
// Optional: DURATION=30s
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	xphone "github.com/x-phone/xphone-go"
)

func main() {
	user, pass, host, target := os.Getenv("SIP_USER"), os.Getenv("SIP_PASS"), os.Getenv("SIP_HOST"), os.Getenv("TARGET")
	if user == "" || pass == "" || host == "" || target == "" {
		fmt.Fprintln(os.Stderr, "need SIP_USER, SIP_PASS, SIP_HOST, TARGET env vars")
		os.Exit(1)
	}
	dur := 30 * time.Second
	if d := os.Getenv("DURATION"); d != "" {
		if p, err := time.ParseDuration(d); err == nil {
			dur = p
		}
	}

	p := xphone.New(
		xphone.WithCredentials(user, pass, host),
		xphone.WithRTPPorts(10000, 20000),
	)
	ctx, cancel := context.WithTimeout(context.Background(), dur+30*time.Second)
	defer cancel()
	if err := p.Connect(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer p.Disconnect()

	dialStart := time.Now()
	call, err := p.Dial(ctx, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer call.End()
	answered := time.Since(dialStart)

	// Send paced silence so the NAT pinhole opens and the far end's
	// symmetric-RTP latches — without outbound media, inbound never arrives
	// from behind NAT. (Real voice agents always talk; the harness must too.)
	go func() {
		silence := make([]int16, 160)
		w := call.PacedPCMWriter()
		for {
			select {
			case <-ctx.Done():
				return
			case w <- silence:
			}
		}
	}()

	var deltas []time.Duration
	var last, firstFrame time.Time
	deadline := time.After(dur)
loop:
	for {
		select {
		case <-deadline:
			break loop
		case frame, ok := <-call.PCMReader():
			if !ok {
				break loop
			}
			_ = frame
			now := time.Now()
			if firstFrame.IsZero() {
				firstFrame = now
			}
			if !last.IsZero() && now.Sub(firstFrame) > 2*time.Second { // warmup discard
				deltas = append(deltas, now.Sub(last))
			}
			last = now
		}
	}

	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	pct := func(q float64) float64 {
		if len(deltas) == 0 {
			return 0
		}
		return ms(deltas[int(q*float64(len(deltas)-1))])
	}
	over40, in1525 := 0, 0
	for _, d := range deltas {
		if d > 40*time.Millisecond {
			over40++
		}
		if d >= 15*time.Millisecond && d <= 25*time.Millisecond {
			in1525++
		}
	}
	out := map[string]any{
		"host":                host,
		"answered_after_ms":   ms(answered),
		"time_to_first_frame": ms(firstFrame.Sub(dialStart)),
		"frames":              len(deltas),
		"p50_ms":              pct(.50),
		"p95_ms":              pct(.95),
		"p99_ms":              pct(.99),
		"max_ms":              func() float64 { if len(deltas) == 0 { return 0 }; return ms(deltas[len(deltas)-1]) }(),
		"pct_over_40ms":       100 * float64(over40) / float64(max(len(deltas), 1)),
		"pct_within_15_25ms":  100 * float64(in1525) / float64(max(len(deltas), 1)),
	}
	j, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(j))
}
