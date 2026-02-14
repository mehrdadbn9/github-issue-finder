#!/bin/bash

echo "==================================================================="
echo "         GOOD FIRST ISSUES FOR CONTRIBUTORS"
echo "==================================================================="
echo ""

echo "=== OFFICIAL GOOD FIRST ISSUES (labeled) ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "
SELECT DISTINCT issue_id, issue_title, score, project_name, category, issue_url 
FROM issue_history 
WHERE labels::text ILIKE '%good first issue%' 
ORDER BY score DESC;
" 2>/dev/null

echo ""
echo "=== HELP WANTED ISSUES ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "
SELECT DISTINCT issue_id, issue_title, score, project_name, category, issue_url 
FROM issue_history 
WHERE labels::text ILIKE '%help wanted%' 
ORDER BY score DESC 
LIMIT 15;
" 2>/dev/null

echo ""
echo "=== TOP ML/AI ISSUES (Beginner Friendly) ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "
SELECT DISTINCT issue_id, issue_title, score, project_name, issue_url 
FROM issue_history 
WHERE category = 'ML/AI'
AND (labels::text ILIKE '%bug%' OR labels::text ILIKE '%enhancement%')
AND score >= 0.75
ORDER BY score DESC 
LIMIT 15;
" 2>/dev/null

echo ""
echo "=== TOP KUBERNETES/DEVOPS ISSUES ==="
docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "
SELECT DISTINCT issue_id, issue_title, score, project_name, issue_url 
FROM issue_history 
WHERE category IN ('Kubernetes', 'CI/CD', 'Monitoring')
AND (labels::text ILIKE '%bug%' OR labels::text ILIKE '%enhancement%')
AND score >= 0.75
ORDER BY score DESC 
LIMIT 15;
" 2>/dev/null

echo ""
echo "==================================================================="
echo "To work on any issue:"
echo "1. Click the issue URL"
echo "2. Read the issue description"
echo "3. Comment that you'd like to work on it"
echo "4. Submit a Pull Request when ready"
echo "==================================================================="
