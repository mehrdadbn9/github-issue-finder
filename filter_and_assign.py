#!/usr/bin/env python3
"""
Filter and identify good contribution issues from database
Excludes issues with: needs more info, question, help wanted (as question)
Includes issues with: clear bug/feature labels, detailed descriptions
"""

import subprocess

# Query database for high-scoring issues
query = """
SELECT 
    issue_id,
    issue_title,
    score,
    comments,
    project_name,
    category,
    labels
FROM issue_history 
WHERE score >= 0.75 
ORDER BY score DESC, discovered_at DESC 
LIMIT 50;
"""

# Execute query
result = subprocess.run(
    [
        "docker",
        "exec",
        "-i",
        "issue-finder-postgres",
        "psql",
        "-U",
        "postgres",
        "-d",
        "issue_finder",
        "-c",
        query,
    ],
    capture_output=True,
    text=True,
)

print("=" * 80)
print("HIGH-SCORING ISSUES FOR POTENTIAL CONTRIBUTION")
print("=" * 80)
print()

# Parse and analyze
lines = result.stdout.strip().split("\n")[2:-1]  # Skip headers
print(f"{'ISSUE':<50} {'SCORE':<8} {'COMMENTS':<10} {'CATEGORY':<15} {'LABELS':<40}")
print("-" * 80)

good_issues = []
bad_issues = []

for line in lines:
    if not line.strip():
        continue

    parts = line.split("|")
    if len(parts) >= 5:
        issue_id = parts[1].strip()
        title = parts[2][:50].strip()
        score = parts[3].strip()
        comments = parts[4].strip()
        category = parts[5].strip() if len(parts) > 5 else ""

        # Analyze for good contribution
        labels_str = parts[6].strip() if len(parts) > 6 else ""
        labels = labels_str.lower() if labels_str else ""

        # Skip criteria
        is_bad = False
        reason = ""

        if (
            "needs more info" in labels
            or "question" in title.lower()
            or "help" in title.lower()
        ):
            is_bad = True
            reason = "Needs more info"
        elif comments == "0" and score < 0.80:
            is_bad = True
            reason = "Low score, no activity"
        elif "question" in title.lower()[:20]:
            is_bad = True
            reason = "Likely a question"

        if is_bad:
            bad_issues.append((issue_id, score, reason))
        else:
            good_issues.append((issue_id, score, comments, category))
            print(f"{issue_id:<50} {score:<8} {comments:<10} {category:<15}")

print()
print("=" * 80)
print(f"GOOD ISSUES FOR CONTRIBUTION: {len(good_issues)}")
print(f"SKIP THESE ISSUES: {len(bad_issues)}")
print("=" * 80)
print()

print("ISSUES TO SKIP (bad for contribution):")
for issue_id, score, reason in bad_issues[:10]:
    print(f"  - {issue_id} (score: {score}): {reason}")

print()
print("RECOMMENDED ACTIONS:")
print("1. Only comment on GOOD issues")
print("2. Skip issues with 'needs more info' label")
print("3. Focus on issues with clear bug/feature descriptions")
print("4. Prefer issues with some comments (indicates activity)")
