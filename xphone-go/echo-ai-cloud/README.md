# Echo AI (Cloud)

A caller dials in, hears a greeting, speaks, and the AI echoes back "You said: ..." in a different voice. Press any DTMF key and it says "You pressed: ...". One binary, no gateway needed.

```
Caller → SIP Trunk/PBX → xphone-go
                              ├── Deepgram STT (WebSocket, live transcription)
                              └── Deepgram TTS (HTTP, spoken response)
```

## Quick start

You need a SIP server (PBX or trunk) to register against. The fastest way is to spin up [xpbx](https://github.com/x-phone/xpbx) — it comes with extensions 1001-1003 pre-created (password: `password123`):

```bash
# Start xpbx (replace with your LAN IP or Tailscale IP)
docker run -d --name xpbx \
  -p 5060:5060/udp -p 5060:5060/tcp \
  -p 8080:8080 \
  -p 10000-10099:10000-10099/udp \
  -e EXTERNAL_IP=$(tailscale ip -4) \
  ghcr.io/x-phone/xpbx:latest

# Run the demo
SIP_USERNAME=1002 \
SIP_PASSWORD=password123 \
SIP_HOST=$(tailscale ip -4) \
DEEPGRAM_API_KEY=your-key \
go run main.go
```

Then call extension 1002 from another extension on the same PBX. You can use any SIP softphone — for example [Zoiper](https://www.zoiper.com/) on your phone. Register as extension 1001 on the same PBX (your phone needs to be on the same network or Tailscale), then dial 1002.

If you have your own PBX or SIP trunk, pass the credentials inline or via a `.env` file:

```bash
SIP_USERNAME=myext \
SIP_PASSWORD=mypass \
SIP_HOST=pbx.example.com \
DEEPGRAM_API_KEY=your-key \
go run main.go
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `SIP_USERNAME` | yes | SIP registration username |
| `SIP_PASSWORD` | yes | SIP registration password |
| `SIP_HOST` | yes | SIP server (PBX or trunk provider) |
| `SIP_TRANSPORT` | no | `udp` (default), `tcp`, or `tls` |
| `DEEPGRAM_API_KEY` | yes | Deepgram API key (STT + TTS) |

## How it works

1. Registers on the SIP host and waits for incoming calls
2. On answer, plays a TTS greeting (female voice)
3. Streams caller audio to Deepgram STT via WebSocket
4. When the caller finishes a sentence, sends "You said: {text}" to Deepgram TTS (male voice) and plays it back
5. DTMF key presses trigger "You pressed: {digit}"
6. Repeats until the caller hangs up
