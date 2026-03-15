#!/usr/bin/env bash
# Creates a SIP trunk in xpbx that routes extension 2000 to the demo's server mode.
# Run this once after "docker compose up -d".
#
# Usage: ./setup-trunk.sh <your-ip>
#   e.g: ./setup-trunk.sh $(tailscale ip -4)

set -e

DEMO_HOST="${1:?Usage: ./setup-trunk.sh <your-ip>}"
DEMO_PORT="${2:-5080}"
XPBX_URL="${XPBX_URL:-http://localhost:8080}"

echo "Waiting for xpbx to be ready..."
until curl -sf "$XPBX_URL/trunks" > /dev/null 2>&1; do sleep 1; done

echo "Creating trunk: echo-demo -> $DEMO_HOST:$DEMO_PORT"
curl -sf -X POST "$XPBX_URL/trunks" \
  -d "name=echo-demo" \
  -d "host=$DEMO_HOST" \
  -d "port=$DEMO_PORT" \
  -d "context=from-trunk" \
  -d "codecs=ulaw"

echo "Creating dialplan: 2000 -> echo-demo trunk"
curl -sf -X POST "$XPBX_URL/dialplan" \
  -d "context=from-internal" \
  -d "exten=2000" \
  -d "priority=1" \
  -d "app=NoOp" \
  -d "appdata=Route 2000 via echo-demo"

curl -sf -X POST "$XPBX_URL/dialplan" \
  -d "context=from-internal" \
  -d "exten=2000" \
  -d "priority=2" \
  -d "app=Dial" \
  -d 'appdata=PJSIP/${EXTEN}@echo-demo,30'

curl -sf -X POST "$XPBX_URL/dialplan" \
  -d "context=from-internal" \
  -d "exten=2000" \
  -d "priority=3" \
  -d "app=Hangup"

echo "Done! Dial 2000 to reach the demo via SIP trunk."
