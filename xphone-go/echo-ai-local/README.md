# Echo AI (Local) — No API Keys Needed

Same echo demo as [echo-ai-cloud](../echo-ai-cloud) but uses open-source STT/TTS running locally in Docker. No cloud APIs, no API keys — everything runs on your machine.

This demo runs **two connection modes** simultaneously, showing both ways xphone-go can receive calls:

```
Caller → xpbx (Asterisk)
           │
           ├── dial 1002 → Phone mode (SIP registration)
           │                     │
           └── dial 2000 → Server mode (SIP trunk)
                                 │
                           xphone-go (this app)
                                 ├── faster-whisper STT (HTTP, batch transcription)
                                 └── Kokoro TTS (HTTP, spoken response)
```

- **Phone mode** — registers as extension 1002 on the PBX, like a softphone
- **Server mode** — listens on port 5080 for SIP trunk calls routed by the PBX (extension 2000)

Both modes produce the same `xphone.Call` interface — the echo logic is shared.

## Prerequisites

- Docker (for Asterisk + STT/TTS services)
- Go 1.21+ (to run the demo)

## Quick start

### 1. Start the stack (Asterisk + STT + TTS)

```bash
EXTERNAL_IP=$(tailscale ip -4) docker compose up -d
```

This starts [xpbx](https://github.com/x-phone/xpbx) (Asterisk + web UI on [localhost:8080](http://localhost:8080), extensions 1001–1003, password `password123`), faster-whisper, and Kokoro. First run downloads models (~150MB whisper-tiny + ~350MB Kokoro).

### 2. Create a SIP trunk for server mode

```bash
./setup-trunk.sh $(tailscale ip -4)
```

This creates a trunk in xpbx that routes extension **2000** to the demo's server mode on port 5080. Only needed once.

### 3. Run the demo

```bash
SIP_USERNAME=1002 \
SIP_PASSWORD=password123 \
SIP_HOST=$(tailscale ip -4) \
go run main.go
```

### 4. Call from a SIP phone

Use any SIP softphone (e.g. [Zoiper](https://www.zoiper.com/)). Register as extension 1001 on the same PBX, then:

- **Dial 1002** — reaches the demo via Phone mode (SIP registration)
- **Dial 2000** — reaches the demo via Server mode (SIP trunk)

Both behave identically — the same echo logic handles both.

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `SIP_USERNAME` | yes | — | SIP registration username |
| `SIP_PASSWORD` | yes | — | SIP registration password |
| `SIP_HOST` | yes | — | SIP server (PBX or trunk) |
| `SIP_TRANSPORT` | no | `udp` | `udp`, `tcp`, or `tls` |
| `SERVER_LISTEN` | no | `0.0.0.0:5080` | Server mode SIP listen address |
| `SERVER_RTP_ADDRESS` | no | auto-detect | IP to advertise in SDP for server mode |
| `STT_URL` | no | `http://localhost:8000` | Whisper server URL |
| `TTS_URL` | no | `http://localhost:8880` | Kokoro server URL |

## How it works

1. **Phone mode** registers on the SIP host as extension 1002
2. **Server mode** starts listening on :5080 for incoming SIP INVITEs
3. xpbx routes calls to 2000 through a SIP trunk pointing at :5080 (created by `setup-trunk.sh`)
4. Both modes share the same call handler:
   - On answer, plays a TTS greeting via Kokoro (`af_bella` voice)
   - Reads caller PCM audio and runs energy-based silence detection (VAD)
   - When the caller finishes speaking (500ms silence), sends the utterance as WAV to faster-whisper for transcription
   - Sends "You said: {text}" to Kokoro TTS (`af_sky` voice) and plays it back
   - DTMF key presses trigger "You pressed: {digit}"
5. Repeats until the caller hangs up

## Performance notes

- First TTS call may be slow (~3s on CPU) as the model loads
- Subsequent calls: <1s latency
- Total RAM: ~2-3 GB for both services
- STT: faster-whisper (tiny.en model, ~39M params, CPU)
- TTS: Kokoro (82M params, CPU, ~3x realtime)
