# framebench — frame-timing load test (loopback)

Measures frame inter-arrival at `call.PCMReader()` — what an STT consumer
sees — across concurrent-call load levels (1/10/25/50), using
[fakepbx](https://github.com/x-phone/fakepbx) for in-process SIP and real
RTP over loopback. No trunks, no Docker, no accounts.

Per call: a far-end goroutine sends 20 ms PCMA frames from its own UDP
socket; the consumer timestamps every decoded PCM frame. Sender-side
cadence is recorded as the control. Prints a JSON summary and writes
`results.json` (p50/p95/p99/max, frames > 40 ms, share within 20 ± 5 ms).

```sh
go mod tidy
go test -run TestFrameTiming -v -timeout 20m
```

Written for the article
[Giving AI agents a real phone line](https://amenophis.dev/writing/voice-agents-real-phone-line/) —
known limitations (single machine, shared scheduler, ~20 s per level) are
discussed there.
