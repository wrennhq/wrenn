#!/usr/bin/env bash
#
# test-host.sh — Integration test for the Wrenn host agent.
#
# Prerequisites:
#   - Host agent running: sudo ./builds/wrenn-agent
#   - Firecracker installed at /usr/local/bin/firecracker
#   - Kernel at /var/lib/wrenn/kernels/vmlinux
#   - Base rootfs at /var/lib/wrenn/images/minimal.ext4 (with envd + wrenn-init baked in)
#
# Usage:
#   ./scripts/test-host.sh [agent_url]
#
# The agent URL defaults to http://localhost:50051.

set -euo pipefail

AGENT="${1:-http://localhost:50051}"
BASE="/hostagent.v1.HostAgentService"
SANDBOX_ID=""

# Colors for output.
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() { echo -e "${RED}FAIL${NC}: $1"; exit 1; }
info() { echo -e "${YELLOW}----${NC}: $1"; }

rpc() {
    local method="$1"
    local body="$2"
    curl -s -X POST \
        -H "Content-Type: application/json" \
        "${AGENT}${BASE}/${method}" \
        -d "${body}"
}

# ──────────────────────────────────────────────────
# Test 1: List sandboxes (should be empty)
# ──────────────────────────────────────────────────
info "Test 1: List sandboxes (expect empty)"

RESULT=$(rpc "ListSandboxes" '{}')
echo "  Response: ${RESULT}"

echo "${RESULT}" | grep -q '"sandboxes"' || echo "${RESULT}" | grep -q '{}' && \
    pass "ListSandboxes returned" || \
    fail "ListSandboxes failed"

# ──────────────────────────────────────────────────
# Test 2: Create a sandbox
# ──────────────────────────────────────────────────
info "Test 2: Create a sandbox"

RESULT=$(rpc "CreateSandbox" '{
    "template": "minimal",
    "vcpus": 1,
    "memoryMb": 512,
    "timeoutSec": 300
}')
echo "  Response: ${RESULT}"

SANDBOX_ID=$(echo "${RESULT}" | python3 -c "import sys,json; print(json.load(sys.stdin)['sandboxId'])" 2>/dev/null) || \
    fail "CreateSandbox did not return sandboxId"

echo "  Sandbox ID: ${SANDBOX_ID}"
pass "Sandbox created: ${SANDBOX_ID}"

# ──────────────────────────────────────────────────
# Test 3: List sandboxes (should have one)
# ──────────────────────────────────────────────────
info "Test 3: List sandboxes (expect one)"

RESULT=$(rpc "ListSandboxes" '{}')
echo "  Response: ${RESULT}"

echo "${RESULT}" | grep -q "${SANDBOX_ID}" && \
    pass "Sandbox ${SANDBOX_ID} found in list" || \
    fail "Sandbox not found in list"

# ──────────────────────────────────────────────────
# Test 4: Execute a command
# ──────────────────────────────────────────────────
info "Test 4: Execute 'echo hello world'"

RESULT=$(rpc "Exec" "{
    \"sandboxId\": \"${SANDBOX_ID}\",
    \"cmd\": \"/bin/sh\",
    \"args\": [\"-c\", \"echo hello world\"],
    \"timeoutSec\": 10
}")
echo "  Response: ${RESULT}"

# stdout is base64-encoded in Connect RPC JSON.
STDOUT=$(echo "${RESULT}" | python3 -c "
import sys, json, base64
r = json.load(sys.stdin)
print(base64.b64decode(r['stdout']).decode().strip())
" 2>/dev/null) || fail "Exec did not return stdout"

[ "${STDOUT}" = "hello world" ] && \
    pass "Exec returned correct output: '${STDOUT}'" || \
    fail "Expected 'hello world', got '${STDOUT}'"

# ──────────────────────────────────────────────────
# Test 5: Execute a multi-line command
# ──────────────────────────────────────────────────
info "Test 5: Execute multi-line command"

RESULT=$(rpc "Exec" "{
    \"sandboxId\": \"${SANDBOX_ID}\",
    \"cmd\": \"/bin/sh\",
    \"args\": [\"-c\", \"echo line1; echo line2; echo line3\"],
    \"timeoutSec\": 10
}")
echo "  Response: ${RESULT}"

LINE_COUNT=$(echo "${RESULT}" | python3 -c "
import sys, json, base64
r = json.load(sys.stdin)
lines = base64.b64decode(r['stdout']).decode().strip().split('\n')
print(len(lines))
" 2>/dev/null)

[ "${LINE_COUNT}" = "3" ] && \
    pass "Multi-line output: ${LINE_COUNT} lines" || \
    fail "Expected 3 lines, got ${LINE_COUNT}"

# ──────────────────────────────────────────────────
# Test 6: Pause the sandbox
# ──────────────────────────────────────────────────
info "Test 6: Pause sandbox"

RESULT=$(rpc "PauseSandbox" "{\"sandboxId\": \"${SANDBOX_ID}\"}")
echo "  Response: ${RESULT}"

# Verify status is paused.
LIST=$(rpc "ListSandboxes" '{}')
echo "${LIST}" | grep -q '"paused"' && \
    pass "Sandbox paused" || \
    fail "Sandbox not in paused state"

# ──────────────────────────────────────────────────
# Test 7: Exec should fail while paused
# ──────────────────────────────────────────────────
info "Test 7: Exec while paused (expect error)"

RESULT=$(rpc "Exec" "{
    \"sandboxId\": \"${SANDBOX_ID}\",
    \"cmd\": \"/bin/echo\",
    \"args\": [\"should fail\"]
}")
echo "  Response: ${RESULT}"

echo "${RESULT}" | grep -qi "not running\|error\|code" && \
    pass "Exec correctly rejected while paused" || \
    fail "Exec should have failed while paused"

# ──────────────────────────────────────────────────
# Test 8: Resume the sandbox
# ──────────────────────────────────────────────────
info "Test 8: Resume sandbox"

RESULT=$(rpc "ResumeSandbox" "{\"sandboxId\": \"${SANDBOX_ID}\"}")
echo "  Response: ${RESULT}"

# Verify status is running.
LIST=$(rpc "ListSandboxes" '{}')
echo "${LIST}" | grep -q '"running"' && \
    pass "Sandbox resumed" || \
    fail "Sandbox not in running state"

# ──────────────────────────────────────────────────
# Test 9: Exec after resume
# ──────────────────────────────────────────────────
info "Test 9: Exec after resume"

RESULT=$(rpc "Exec" "{
    \"sandboxId\": \"${SANDBOX_ID}\",
    \"cmd\": \"/bin/sh\",
    \"args\": [\"-c\", \"echo resumed ok\"],
    \"timeoutSec\": 10
}")
echo "  Response: ${RESULT}"

STDOUT=$(echo "${RESULT}" | python3 -c "
import sys, json, base64
r = json.load(sys.stdin)
print(base64.b64decode(r['stdout']).decode().strip())
" 2>/dev/null) || fail "Exec after resume failed"

[ "${STDOUT}" = "resumed ok" ] && \
    pass "Exec after resume works: '${STDOUT}'" || \
    fail "Expected 'resumed ok', got '${STDOUT}'"

# ──────────────────────────────────────────────────
# Test 10: Destroy the sandbox
# ──────────────────────────────────────────────────
info "Test 10: Destroy sandbox"

RESULT=$(rpc "DestroySandbox" "{\"sandboxId\": \"${SANDBOX_ID}\"}")
echo "  Response: ${RESULT}"
pass "Sandbox destroyed"

# ──────────────────────────────────────────────────
# Test 11: List sandboxes (should be empty again)
# ──────────────────────────────────────────────────
info "Test 11: List sandboxes (expect empty)"

RESULT=$(rpc "ListSandboxes" '{}')
echo "  Response: ${RESULT}"

echo "${RESULT}" | grep -q "${SANDBOX_ID}" && \
    fail "Destroyed sandbox still in list" || \
    pass "Sandbox list is clean"

# ──────────────────────────────────────────────────
# Test 12: Destroy non-existent sandbox (expect error)
# ──────────────────────────────────────────────────
info "Test 12: Destroy non-existent sandbox (expect error)"

RESULT=$(rpc "DestroySandbox" '{"sandboxId": "sb-nonexist"}')
echo "  Response: ${RESULT}"

echo "${RESULT}" | grep -qi "not found\|error\|code" && \
    pass "Correctly rejected non-existent sandbox" || \
    fail "Should have returned error for non-existent sandbox"

echo ""
echo -e "${GREEN}All tests passed!${NC}"
