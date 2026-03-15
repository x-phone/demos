# Echo AI (Local) — No API Keys Needed

Same echo demo as [echo-ai-cloud](../echo-ai-cloud) but uses open-source STT/TTS running locally in Docker. No cloud APIs, no API keys — everything runs on your machine.

```
Caller → SIP Trunk/PBX → xphone-go
                              ├── faster-whisper STT (HTTP, batch transcription)
                              └── Kokoro TTS (HTTP, spoken response)
```

## Prerequisites

- Docker (for Asterisk + STT/TTS services)
- Go 1.21+ (to run the demo)

## Quick start

### 1. Start the stack (Asterisk + STT + TTS)

```bash
EXTERNAL_IP=$(tailscale ip -4) docker compose up -d
```

This starts [xpbx](https://github.com/x-phone/xpbx) (Asterisk + web UI on [localhost:8080](http://localhost:8080), extensions 1001–1003, password `password123`), faster-whisper, and Kokoro. First run downloads models (~150MB whisper-tiny + ~350MB Kokoro).

### 2. Run the demo

```bash
SIP_USERNAME=1002 \
SIP_PASSWORD=password123 \
SIP_HOST=$(tailscale ip -4) \
go run main.go
```

### 3. Call extension 1002 from a SIP phone

Use any SIP softphone (e.g. [Zoiper](https://www.zoiper.com/)). Register as extension 1001 on the same PBX, then dial 1002.

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `SIP_USERNAME` | yes | — | SIP registration username |
| `SIP_PASSWORD` | yes | — | SIP registration password |
| `SIP_HOST` | yes | — | SIP server (PBX or trunk) |
| `SIP_TRANSPORT` | no | `udp` | `udp`, `tcp`, or `tls` |
| `STT_URL` | no | `http://localhost:8000` | Whisper server URL |
| `TTS_URL` | no | `http://localhost:8880` | Kokoro server URL |

## How it works

1. Registers on the SIP host and waits for incoming calls
2. On answer, plays a TTS greeting via Kokoro (`af_bella` voice)
3. Reads caller PCM audio and runs energy-based silence detection (VAD)
4. When the caller finishes speaking (500ms silence), sends the utterance as WAV to faster-whisper for transcription
5. Sends "You said: {text}" to Kokoro TTS (`af_sky` voice) and plays it back
6. DTMF key presses trigger "You pressed: {digit}"
7. Repeats until the caller hangs up

## Performance notes

- First TTS call may be slow (~3s on CPU) as the model loads
- Subsequent calls: <1s latency
- Total RAM: ~2-3 GB for both services
- STT: faster-whisper (tiny.en model, ~39M params, CPU)
- TTS: Kokoro (82M params, CPU, ~3x realtime)
