#!/usr/bin/env python3
"""Server-side: verify tracked issues upstream + scan new candidates.
Runs on 147.45.72.189. Reads GITHUB_TOKEN from /opt/issue-finder/.env (never prints it).
Updates has_pr/has_assignee flags in OUR OWN Postgres only - no GitHub writes.
Usage: python3 verify_tracked_issues.py
"""
import json, os, subprocess, sys, time, urllib.request, urllib.parse
from datetime import datetime, timedelta

ENV = {}
with open("/opt/issue-finder/.env") as f:
    for line in f:
        line = line.strip()
        if "=" in line and not line.startswith("#"):
            k, v = line.split("=", 1)
            ENV[k.strip()] = v.strip()
TOKEN = ENV.get("GITHUB_TOKEN", "")
if not TOKEN:
    sys.exit("no GITHUB_TOKEN")

def gh(path, params=None):
    url = "https://api.github.com" + path
    if params:
        url += "?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + TOKEN,
        "Accept": "application/vnd.github+json",
        "User-Agent": "issue-finder-verify",
    })
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return {"_error": e.code, "_msg": e.read().decode()[:200]}

def wait_for_search_quota():
    """Wait for GitHub's search quota to reset when it is nearly exhausted."""
    rate = gh("/rate_limit")
    search = rate.get("resources", {}).get("search", {})
    if search.get("remaining", 3) <= 2:
        reset = search.get("reset", time.time())
        wait = reset + 2 - time.time()
        while wait > 0:
            time.sleep(min(wait, 60))
            wait = reset + 2 - time.time()

def search_issues(params):
    """Search issues with rate-limit handling and one retry for HTTP 403."""
    for attempt in range(2):
        wait_for_search_quota()
        result = gh("/search/issues", params)
        if not (isinstance(result, dict) and result.get("_error") == 403):
            return result
        if attempt == 0:
            time.sleep(30)
    print("WARNING: GitHub search API returned HTTP 403 twice; returning empty result")
    return {"total_count": 0, "items": []}

def psql(q):
    out = subprocess.run(
        ["docker", "exec", "issue-finder-postgres-1", "psql", "-U", "issue-finder",
         "-d", "issue-finder", "-t", "-A", "-F", "|", "-c", q],
        capture_output=True, text=True)
    return [l for l in out.stdout.strip().splitlines() if l]

open_prs_by_repo = {}

def track_repo(org, name, num):
    """Count open linked PRs referencing this issue."""
    repo = (org, name)
    if repo not in open_prs_by_repo:
        r = search_issues({"q": f"repo:{org}/{name} is:pr state:open", "per_page": 100})
        if isinstance(r, dict) and "_error" in r:
            open_prs_by_repo[repo] = None
            return None
        open_prs_by_repo[repo] = r.get("items", [])
    if open_prs_by_repo[repo] is None:
        return None
    issue_ref = "#" + str(num)
    return sum(issue_ref in ((pr.get("title") or "") + "\n" + (pr.get("body") or ""))
               for pr in open_prs_by_repo[repo])

print("=== 1. TRACKED ISSUES UPSTREAM VERIFICATION ===")
rows = psql("SELECT id, project_org, project_name, issue_number, issue_title, score, status, has_pr, has_assignee FROM tracked_issues ORDER BY score DESC")
updated = stale = 0
new_flagged_pr = new_flagged_assignee = closed = 0
for row in rows:
    parts = row.split("|")
    tid, org, name, num = parts[0], parts[1], parts[2], parts[3]
    title = "|".join(parts[4:-4])
    score, status, has_pr, has_assignee = parts[-4], parts[-3], parts[-2], parts[-1]
    iss = gh(f"/repos/{org}/{name}/issues/{num}")
    if isinstance(iss, dict) and "_error" in iss:
        print(f"[{tid}] {org}/{name}#{num} API ERROR {iss['_error']} {iss['_msg'][:80]}")
        continue
    state = iss.get("state", "?")
    assignees = [a["login"] for a in (iss.get("assignees") or [])]
    pr_count = track_repo(org, name, num)
    pr_flag = bool(pr_count)
    assignee_flag = bool(assignees)
    if status == "notified" and state == "closed":
        stale += 1
        closed += 1
        psql(f"UPDATE tracked_issues SET status='completed', completed_at=NOW() WHERE id={tid}")
    if pr_flag and has_pr != "t":
        psql(f"UPDATE tracked_issues SET has_pr=TRUE WHERE id={tid}")
        new_flagged_pr += 1
    if assignee_flag and has_assignee != "t":
        psql(f"UPDATE tracked_issues SET has_assignee=TRUE WHERE id={tid}")
        new_flagged_assignee += 1
    if not (pr_flag or assignee_flag) and state == "open" and (has_pr == "t" or has_assignee == "t"):
        updated += 1
    print(f"{'OPEN ' if state=='open' else 'CLOSED'} {org}/{name}#{num} score={float(score):.2f} assignees={','.join(assignees) or '-'} openPR={pr_count} title={title[:60]}")
print(f"\nsummary: {len(rows)} tracked | closed upstream: {closed} | new has_pr flags: {new_flagged_pr} | new has_assignee flags: {new_flagged_assignee}")

print("\n=== 2. NEW CANDIDATE SCAN (8 config repos, last 72h, open, unassigned) ===")
repos = [
    ("VictoriaMetrics", "VictoriaMetrics"), ("VictoriaMetrics", "operator"),
    ("slok", "sloth"), ("strimzi", "strimzi-kafka-operator"),
    ("rabbitmq", "cluster-operator"), ("argoproj", "argo-cd"),
    ("kubernetes", "kubernetes"), ("etcd-io", "etcd"),
]
since = (datetime.utcnow() - timedelta(hours=72)).strftime("%Y-%m-%dT%H:%M:%SZ")
tracked = set()
for row in psql("SELECT project_org, project_name, issue_number FROM tracked_issues"):
    o, n, num = row.split("|")
    tracked.add(f"{o}/{n}#{num}")
new_found = []
for org, name in repos:
    r = search_issues({"q": f"repo:{org}/{name} is:issue is:open no:assignee created:>{since}",
                       "sort": "created", "order": "desc", "per_page": 10})
    if isinstance(r, dict) and "_error" in r:
        print(f"{org}/{name}: search error {r['_error']}")
        continue
    for item in r.get("items", []):
        key = f"{org}/{name}#{item['number']}"
        if key in tracked:
            continue
        pr = track_repo(org, name, item["number"])
        if pr is None or pr > 0:
            continue
        score = round(0.2 + min(item.get("comments", 0), 10) * 0.05 + (0.15 if item.get("labels") else 0), 2)
        new_found.append((org, name, item["number"], item["title"][:70], score, item["html_url"]))
for org, name, num, title, score, url in new_found:
    print(f"{org}/{name}#{num} score={score:.2f} {title}\n    {url}")
print(f"\nnew candidates: {len(new_found)}")
