# trunkbench — frame timing over a real SIP trunk

Companion to [framebench](../framebench/): same metric
(`PCMReader()` inter-arrival), measured over a live trunk. Dials a real
number and measures the far end's audio (a voicemail greeting works) while
streaming paced silence upstream — without outbound media, a receive-only
client behind NAT never opens a pinhole and gets zero frames.

```sh
SIP_USER=... SIP_PASS=... SIP_HOST=sip.telnyx.com \
TARGET=+1415XXXXXXX DURATION=45s go run .
```

Prints a JSON summary: answer time, time to first frame, and the
p50/p95/p99/max distribution. One call is a sanity check, not a network
study — see the article for how (not) to interpret it.
