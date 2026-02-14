#!/bin/bash

set -e

PROJECT_DIR="/home/tapsell/github-issue-finder"
LOG_FILE="/home/tapsell/github-issue-finder/scheduled_push.log"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

log "Starting scheduled push..."

cd "$PROJECT_DIR" || {
    log "ERROR: Failed to change to project directory"
    exit 1
}

git status >> "$LOG_FILE" 2>&1

if git diff-index --quiet HEAD --; then
    log "No changes to commit"
    exit 0
fi

log "Staging changes..."
git add -A >> "$LOG_FILE" 2>&1

COMMIT_MSG="Production grade improvements - $(date '+%Y-%m-%d %H:%M')"

log "Creating commit..."
git commit -m "$COMMIT_MSG" >> "$LOG_FILE" 2>&1

log "Pushing to remote..."
git push origin master >> "$LOG_FILE" 2>&1

log "Push completed successfully"

echo "Push completed: $COMMIT_MSG"