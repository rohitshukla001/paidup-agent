#!/usr/bin/env bash
# Runs the demo script (Section 3.2 of docs/implementation-plan.md) against
# a running PaidUp server with real PayPal sandbox and Gemini settings.
#
#   BASE_URL=https://paidup.onrender.com DEMO_PASSWORD=... scripts/e2e.sh
#
# Step 5 (paying the invoice) needs a person: the script prints the payer
# link, then waits up to 5 minutes for the invoice to show PAID.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
: "${DEMO_PASSWORD:?set DEMO_PASSWORD}"
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
api() { curl -sS --fail-with-body -b "$JAR" -c "$JAR" "$@"; }
# field NAME: prints the first "NAME":"value" in stdin.
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -n1; }

step "0. Health check"
[ "$(curl -sS "$BASE_URL/healthz")" = "ok" ] || fail "healthz"

step "1. Log in"
api -o /dev/null -X POST "$BASE_URL/api/login" \
  -H 'Content-Type: application/json' \
  -d "{\"password\":\"$DEMO_PASSWORD\"}" || fail "login"
api -o /dev/null "$BASE_URL/api/session" || fail "session cookie"

step "2. Ask for the invoice"
CHAT="$(api -N -X POST "$BASE_URL/api/chat" \
  -F 'text=Bill Jane at jane@example.com 450 USD for a landing page, due in 7 days.')"
echo "$CHAT" | grep -q '^event: action.proposed' || {
  echo "$CHAT"; fail "no invoice card"; }
ACTION_ID="$(echo "$CHAT" | grep -A1 '^event: action.proposed' | field id)"
echo "card: $ACTION_ID"

step "3. Approve the card"
RESULT="$(api -N -X POST "$BASE_URL/api/actions/$ACTION_ID/approve")"
echo "$RESULT" | grep -q '"status":"DONE"' || { echo "$RESULT"; fail "approve"; }
INVOICE_ID="$(echo "$RESULT" | sed 's/.*"invoice":{"id":"\([^"]*\)".*/\1/;t;d' | head -n1)"
PAYER_URL="$(echo "$RESULT" | field payer_view_url)"
[ -n "$PAYER_URL" ] || fail "no payer link"
echo "invoice: $INVOICE_ID"

step "4-5. Pay the invoice with the sandbox Personal account"
echo "Open: $PAYER_URL"
echo "Waiting up to 5 minutes for PAID..."
for _ in $(seq 1 60); do
  STATUS="$(api -X POST "$BASE_URL/api/invoices/$INVOICE_ID/refresh" | field status)"
  [ "$STATUS" = "PAID" ] && break
  sleep 5
done
[ "$STATUS" = "PAID" ] || fail "invoice is $STATUS, not PAID"

step "6. Dashboard shows PAID"
api "$BASE_URL/api/invoices" | grep -q "\"id\":\"$INVOICE_ID\",[^}]*\"status\":\"PAID\"" \
  || fail "dashboard list"

step "7. Upload a sample photo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PHOTO="$(api -N -X POST "$BASE_URL/api/chat" -F 'text=' \
  -F "image=@$ROOT/testdata/samples/06-handwritten-quote.png;type=image/png")"
echo "$PHOTO" | grep -q 'maria.lopez@example.com' || {
  echo "$PHOTO"; fail "photo was not read"; }

step "8. Ask who owes money"
ANSWER="$(api -N -X POST "$BASE_URL/api/chat" -F 'text=Who owes me money?')"
echo "$ANSWER" | grep -q '^event: message.delta' || { echo "$ANSWER"; fail "no answer"; }
echo "$ANSWER" | grep '^data:' | field text

printf '\nE2E PASSED\n'
