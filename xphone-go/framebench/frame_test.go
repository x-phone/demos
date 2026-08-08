package framebench

// Frame-timing-under-load experiment for xphone-go.
// For each load level (concurrent calls), every call has a far-end goroutine
// sending 20 ms PCMA frames over loopback RTP; the consumer times the
// inter-arrival of decoded PCM frames from call.PCMReader() — i.e. exactly
// what an STT pipeline would see. Sender-side cadence is recorded as control.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/x-phone/fakepbx"
	xphone "github.com/x-phone/xphone-go"
)

const (
	frameDur   = 20 * time.Millisecond
	warmup     = 2 * time.Second  // discard first seconds (pipeline start)
	measureDur = 20 * time.Second // measured window per level
)

type stats struct {
	N          int     `json:"frames"`
	P50ms      float64 `json:"p50_ms"`
	P95ms      float64 `json:"p95_ms"`
	P99ms      float64 `json:"p99_ms"`
	MaxMs      float64 `json:"max_ms"`
	PctOver40  float64 `json:"pct_over_40ms"`
	PctIn15to25 float64 `json:"pct_within_15_25ms"`
}

func summarize(deltas []time.Duration) stats {
	if len(deltas) == 0 {
		return stats{}
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	pct := func(q float64) float64 { return ms(deltas[int(q*float64(len(deltas)-1))]) }
	over40, in1525 := 0, 0
	for _, d := range deltas {
		if d > 40*time.Millisecond {
			over40++
		}
		if d >= 15*time.Millisecond && d <= 25*time.Millisecond {
			in1525++
		}
	}
	return stats{
		N: len(deltas), P50ms: pct(.50), P95ms: pct(.95), P99ms: pct(.99),
		MaxMs: ms(deltas[len(deltas)-1]),
		PctOver40:  100 * float64(over40) / float64(len(deltas)),
		PctIn15to25: 100 * float64(in1525) / float64(len(deltas)),
	}
}

type result struct {
	Load      int   `json:"concurrent_calls"`
	Recv      stats `json:"recv"`   // PCMReader inter-arrival (what STT sees)
	Send      stats `json:"send"`   // far-end send cadence (control)
	GoMaxProc int   `json:"gomaxprocs"`
}

// farEnd streams PCMA silence frames at 20ms cadence to xphone's RTP port and
// records actual send times (ticker jitter is real and must be the control).
func farEnd(ctx context.Context, conn net.PacketConn, dst net.Addr, sendTimes *[]time.Time, mu *sync.Mutex) {
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0xD5
	}
	seq, ts := uint16(1), uint32(160)
	tick := time.NewTicker(frameDur)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			pkt := &rtp.Packet{Header: rtp.Header{
				Version: 2, PayloadType: 8, SequenceNumber: seq, Timestamp: ts, SSRC: 0xBEEF,
			}, Payload: payload}
			data, _ := pkt.Marshal()
			conn.WriteTo(data, dst)
			mu.Lock()
			*sendTimes = append(*sendTimes, time.Now())
			mu.Unlock()
			seq++
			ts += 160
		}
	}
}

// audioPort extracts the m=audio port from an SDP body.
func audioPort(sdp string) int {
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=audio ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				p, _ := strconv.Atoi(fields[1])
				return p
			}
		}
	}
	return 0
}

func runLevel(t *testing.T, load int) result {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	recvMu, sendMu := &sync.Mutex{}, &sync.Mutex{}
	var allRecvDeltas []time.Duration
	var allSendDeltas []time.Duration

	for i := 0; i < load; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			pbx := fakepbx.NewFakePBX(t, fakepbx.WithAuth("1001", "test"))
			conn, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			port := conn.LocalAddr().(*net.UDPAddr).Port

			pbx.OnInvite(func(inv *fakepbx.Invite) {
				inv.Trying()
				inv.Answer(fakepbx.SDP("127.0.0.1", port, fakepbx.PCMA))
			})

			host, portStr, _ := net.SplitHostPort(pbx.Addr())
			p := xphone.New(
				xphone.WithCredentials("1001", "test", net.JoinHostPort(host, portStr)),
				xphone.WithTransport("udp", nil),
			)
			cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
			defer ccancel()
			if err := p.Connect(cctx); err != nil {
				t.Errorf("call %d connect: %v", idx, err)
				return
			}
			defer p.Disconnect()

			call, err := p.Dial(cctx, "9"+strconv.Itoa(idx))
			if err != nil {
				t.Errorf("call %d dial: %v", idx, err)
				return
			}
			defer call.End()

			// far end: stream frames to xphone's negotiated RTP port (from local SDP)
			xport := audioPort(call.LocalSDP())
			if xport == 0 {
				t.Errorf("call %d: no audio port in local SDP", idx)
				return
			}
			raddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: xport}
			var sendTimes []time.Time
			go farEnd(ctx, conn, raddr, &sendTimes, sendMu)

			// consumer: time PCM frame arrivals
			start := time.Now()
			var last time.Time
			var deltas []time.Duration
			for {
				select {
				case <-ctx.Done():
					goto done
				case frame, ok := <-call.PCMReader():
					if !ok {
						goto done
					}
					_ = frame
					now := time.Now()
					if !last.IsZero() && now.Sub(start) > warmup {
						deltas = append(deltas, now.Sub(last))
					}
					last = now
				}
			}
		done:
			recvMu.Lock()
			allRecvDeltas = append(allRecvDeltas, deltas...)
			recvMu.Unlock()
			sendMu.Lock()
			sd := make([]time.Duration, 0, len(sendTimes))
			for j := 1; j < len(sendTimes); j++ {
				sd = append(sd, sendTimes[j].Sub(sendTimes[j-1]))
			}
			allSendDeltas = append(allSendDeltas, sd...)
			sendMu.Unlock()
		}(i)
	}

	// let the level run: warmup + measurement, then stop
	time.Sleep(warmup + measureDur)
	cancel()
	wg.Wait()

	return result{Load: load, Recv: summarize(allRecvDeltas), Send: summarize(allSendDeltas), GoMaxProc: runtime.GOMAXPROCS(0)}
}

func TestFrameTiming(t *testing.T) {
	var results []result
	for _, load := range []int{1, 10, 25, 50} {
		t.Logf("=== load %d concurrent calls ===", load)
		r := runLevel(t, load)
		t.Logf("load=%d recv p50=%.2fms p95=%.2fms p99=%.2fms max=%.1fms over40=%.3f%%",
			r.Load, r.Recv.P50ms, r.Recv.P95ms, r.Recv.P99ms, r.Recv.MaxMs, r.Recv.PctOver40)
		results = append(results, r)
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile("results.json", out, 0644)
	fmt.Println(string(out))
}
