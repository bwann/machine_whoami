#!/bin/bash
#
# Smoke test: run machine_whoami against live Netbox and validate output.
# Use after Netbox upgrades to catch API changes before they hit production.
#
# Usage: ./scripts/smoke-test.sh [-config path/to/config.yaml]
#
set -euo pipefail

CONFIG="${1:-machine_whoami.yaml}"
BINARY="./machine_whoami"

if [[ ! -f "$BINARY" ]]; then
    echo "FAIL: binary not found at $BINARY — run 'go build -o machine_whoami main.go' first"
    exit 1
fi

if [[ ! -f "$CONFIG" ]]; then
    echo "FAIL: config not found at $CONFIG"
    exit 1
fi

# Extract output_dir from config
OUTPUT_DIR=$(grep '^output_dir:' "$CONFIG" | awk '{print $2}')
if [[ -z "$OUTPUT_DIR" ]]; then
    echo "FAIL: could not read output_dir from $CONFIG"
    exit 1
fi

echo "=== Running machine_whoami ==="
"$BINARY" -config "$CONFIG" 2>&1 | tee /tmp/machine_whoami_smoke.log

echo ""
echo "=== Validating output ==="

ERRORS=0

# Check that at least one output file was written
FILE_COUNT=$(find "$OUTPUT_DIR" -name '*.txt' -not -name 'failures.log' | wc -l)
if [[ "$FILE_COUNT" -eq 0 ]]; then
    echo "FAIL: no .txt files written to $OUTPUT_DIR"
    ERRORS=$((ERRORS + 1))
else
    echo "OK: $FILE_COUNT device files written"
fi

# Check a sample file for expected NAME=VALUE format
SAMPLE=$(find "$OUTPUT_DIR" -name '*.txt' -not -name 'failures.log' | head -1)
if [[ -n "$SAMPLE" ]]; then
    echo "--- Sample file: $SAMPLE ---"
    cat "$SAMPLE"
    echo "---"

    REQUIRED_FIELDS="DEVICE_NAME DEVICE_REGION DEVICE_DATACENTER DEVICE_RACK DEVICE_ASN DEVICE_SUBNET4 DEVICE_SUBNET6 DEVICE_STATUS DEVICE_WHOAMI_GENERATED_AT"
    for FIELD in $REQUIRED_FIELDS; do
        if ! grep -q "^${FIELD}=" "$SAMPLE"; then
            echo "FAIL: missing $FIELD in $SAMPLE"
            ERRORS=$((ERRORS + 1))
        fi
    done

    # Verify DEVICE_NAME is not empty
    DEVICE_NAME=$(grep '^DEVICE_NAME=' "$SAMPLE" | cut -d'"' -f2)
    if [[ -z "$DEVICE_NAME" ]]; then
        echo "FAIL: DEVICE_NAME is empty in $SAMPLE"
        ERRORS=$((ERRORS + 1))
    else
        echo "OK: DEVICE_NAME=$DEVICE_NAME"
    fi

    # Verify DEVICE_DATACENTER is not empty
    DEVICE_DC=$(grep '^DEVICE_DATACENTER=' "$SAMPLE" | cut -d'"' -f2)
    if [[ -z "$DEVICE_DC" ]]; then
        echo "FAIL: DEVICE_DATACENTER is empty in $SAMPLE"
        ERRORS=$((ERRORS + 1))
    else
        echo "OK: DEVICE_DATACENTER=$DEVICE_DC"
    fi

    # Verify DEVICE_STATUS is not empty
    DEVICE_STATUS=$(grep '^DEVICE_STATUS=' "$SAMPLE" | cut -d'"' -f2)
    if [[ -z "$DEVICE_STATUS" ]]; then
        echo "FAIL: DEVICE_STATUS is empty in $SAMPLE"
        ERRORS=$((ERRORS + 1))
    else
        echo "OK: DEVICE_STATUS=$DEVICE_STATUS"
    fi

    # Verify DEVICE_WHOAMI_GENERATED_AT is a plausible epoch (not in the future)
    GENERATED_AT=$(grep '^DEVICE_WHOAMI_GENERATED_AT=' "$SAMPLE" | cut -d= -f2)
    if [[ ! "$GENERATED_AT" =~ ^[0-9]+$ ]] || (( GENERATED_AT > $(date +%s) )); then
        echo "FAIL: DEVICE_WHOAMI_GENERATED_AT invalid ('$GENERATED_AT') in $SAMPLE"
        ERRORS=$((ERRORS + 1))
    else
        echo "OK: DEVICE_WHOAMI_GENERATED_AT=$GENERATED_AT ($(date -d "@$GENERATED_AT"))"
    fi
fi

# Check for failures.log
if [[ -f "$OUTPUT_DIR/failures.log" ]]; then
    FAIL_COUNT=$(wc -l < "$OUTPUT_DIR/failures.log")
    echo "WARN: failures.log exists with $FAIL_COUNT entries"
    head -5 "$OUTPUT_DIR/failures.log"
fi

# Check the run log for obvious problems
if grep -q "Processing 0 devices" /tmp/machine_whoami_smoke.log; then
    echo "FAIL: 0 devices processed — likely an API field change"
    ERRORS=$((ERRORS + 1))
fi

echo ""
if [[ "$ERRORS" -gt 0 ]]; then
    echo "=== SMOKE TEST FAILED ($ERRORS errors) ==="
    exit 1
else
    echo "=== SMOKE TEST PASSED ==="
fi
