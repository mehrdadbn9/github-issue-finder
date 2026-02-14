#!/bin/sh

set -e

PID=$(pgrep -f "github-issue-finder" || echo "")

if [ -z "$PID" ]; then
    echo "ERROR: github-issue-finder process not running"
    exit 1
fi

if [ -f /app/logs/app.log ]; then
    LAST_LOG=$(tail -1 /app/logs/app.log 2>/dev/null || echo "")
    if [ -n "$LAST_LOG" ]; then
        echo "OK: Process running (PID: $PID)"
        echo "Last log: $LAST_LOG"
    else
        echo "OK: Process running (PID: $PID)"
    fi
else
    echo "OK: Process running (PID: $PID)"
fi

exit 0