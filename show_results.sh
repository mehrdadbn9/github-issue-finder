#!/bin/bash

echo "=== GITHUB ISSUE FINDER - LOCAL RESULTS ==="
echo ""
echo "=== DATABASE STATISTICS ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "SELECT 'Total Issues' as metric, COUNT(*) as count FROM issue_history UNION ALL SELECT 'Seen Issues', COUNT(*) FROM seen_issues;" 2>/dev/null

echo ""
echo "=== TOP 20 HIGHEST SCORING ISSUES ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "SELECT DISTINCT issue_id, issue_title, score, project_name, issue_url FROM issue_history ORDER BY score DESC LIMIT 20;" 2>/dev/null

echo ""
echo "=== GOOD FIRST ISSUES ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "SELECT DISTINCT issue_id, issue_title, score, project_name, issue_url FROM issue_history WHERE labels::text ILIKE '%good first issue%' ORDER BY score DESC;" 2>/dev/null

echo ""
echo "=== NOTIFICATIONS LOG (Last 10) ==="
if [ -f "runtime/logs/notifications.log" ]; then
    tail -10 runtime/logs/notifications.log
else
    echo "No notifications log found"
fi

echo ""
echo "=== ISSUES LOG ==="
if [ -f "runtime/logs/issues.log" ]; then
    cat runtime/logs/issues.log
else
    echo "No issues log found"
fi
