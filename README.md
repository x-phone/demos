# x-phone demos

Working examples that show how to build voice applications with the [x-phone](https://github.com/x-phone) ecosystem. Each demo is self-contained with its own README, source code, and setup instructions.

## Demos

### [xphone-go](xphone-go/) — using the Go library directly

| Demo | What it does | STT / TTS | API keys? |
|---|---|---|---|
| [echo-ai-cloud](xphone-go/echo-ai-cloud/) | Answers a call, echoes back what the caller says | Deepgram (streaming WebSocket STT + HTTP TTS) | Yes (Deepgram) |
| [echo-ai-local](xphone-go/echo-ai-local/) | Same, but fully local — no cloud APIs | faster-whisper + Kokoro (Docker) | No |

### Benchmarks

| Bench | What it measures | Needs |
|---|---|---|
| [framebench](xphone-go/framebench/) | Frame-timing distribution at 1–50 concurrent calls, loopback via fakepbx | nothing — `go test` |
| [trunkbench](xphone-go/trunkbench/) | Same metric over a live SIP trunk | trunk credentials |

<!-- Future sections:
### [xbridge](xbridge/) — using the voice gateway (any language)
### [xpbx](xpbx/) — PBX configuration and routing
-->

## The x-phone ecosystem

```
Real Phone Network (PSTN / SIP trunk)
        │
        ▼
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│    xpbx      │────▶│   xbridge    │────▶│   Your App   │
│  (PBX + UI)  │     │  (gateway)   │     │ (any language)│
└──────────────┘     └──────────────┘     └──────────────┘
        │
        ▼
┌──────────────┐
│  xphone-go   │  ← or use the library directly (Go / Rust)
│  xphone-rust │
└──────────────┘
```

| Project | What it is | When to use it |
|---|---|---|
| [xphone-go](https://github.com/x-phone/xphone-go) | Go library — SIP calls as `[]int16` PCM | Go apps that need direct call control |
| [xphone-rust](https://github.com/x-phone/xphone-rust) | Rust library — same API, pure Rust SIP/RTP | Rust apps that need direct call control |
| [xbridge](https://github.com/x-phone/xbridge) | Voice gateway — SIP to WebSocket + REST | Apps in any language (Python, Node, etc.) |
| [xpbx](https://github.com/x-phone/xpbx) | Self-hosted PBX with web UI | Multi-extension routing, voicemail, trunks |

## Prerequisites

All demos need a SIP server to register against. The quickest option is [xpbx](https://github.com/x-phone/xpbx) in Docker — it comes with extensions 1001-1003 pre-created (password: `password123`). See each demo's README for specific setup instructions.

To place test calls, use any SIP softphone (e.g. [Zoiper](https://www.zoiper.com/)).

## Contributing

To add a new demo, create a directory under the appropriate ecosystem component (e.g. `xphone-go/`, `xbridge/`) with:

- `main.go` (or equivalent entry point)
- `README.md` with quick start, env vars, and how-it-works
- `.env.example` if the demo needs configuration
- Update this README's demo table
