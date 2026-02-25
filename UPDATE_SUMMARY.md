# GitHub Issue Finder - Update Summary

## 1. Filter Criteria Changes

### Updated Label Filtering
- **Now requires**: `good first issue` AND (`confirmed` OR `triage/accepted`) labels
- **Removed**: `help wanted` label filtering as primary criterion
- **Go projects**: Now searches ANY project written in Go, not just `golang/go`

### New Functions in `issue_analyzer.go`
- `hasConfirmedLabel()` - Checks for confirmed/triage/accepted labels
- `hasGoodFirstIssueLabel()` - Checks for good first issue variants
- `CheckFilterCriteria()` - Returns comprehensive filter status
- `MeetsAssignmentCriteria()` - Validates if issue is eligible for assignment

---

## 2. Scoring Changes Summary

### Enhanced Scoring (`enhanced_scorer.go`)
- **Good first issue**: +0.30 (up from 0.25)
- **Confirmed/triage/accepted label**: +0.35 (NEW - highest bonus)
- **Help wanted**: +0.10 (reduced from 0.20, now secondary)

### Additional Bonuses in `main.go` (`ScoreIssue`)
- **No assignee**: +0.15
- **No linked PR**: +0.10  
- **Confirmed label**: +0.25
- **Stale but not active (1-6 months, ≤3 comments)**: +0.10

### Penalty Adjustments
- Needs triage penalty remains: -0.15
- Cloud provider penalty remains: -0.50
- Blocked/waiting penalty remains: -0.20

---

## 3. Email Notifications (NEW Issues Only)

### How It Works
1. `ProcessNewIssueNotifications()` in `main.go` processes only eligible issues
2. Checks `WasAlreadyNotified()` via anti-spam manager
3. Records notification in database with timestamp
4. Only sends email for truly NEW issues

### Tracking
- `notification_log` table tracks all notifications with timestamps
- `tracked_issues.notified_at` field marks notification time
- Cooldown period prevents re-notification

---

## 4. Auto-Assignment Logic

### Eligibility Criteria (ALL must be true)
- Label: "good first issue"
- Label: "confirmed" OR "triage/accepted"  
- No current assignee
- No existing PR linked to the issue
- Issue state: open

### Process (`assignment_manager.go`)
1. `IsAssignmentCandidate()` validates eligibility
2. `CheckForLinkedPR()` searches for PRs mentioning issue number
3. `AskForAssignment()`:
   - Checks spam limits (max 5/day)
   - Checks cooldown (30 min between comments on same repo)
   - Prompts user (unless auto-mode enabled)
   - Posts comment: "Hi, I'd like to work on this issue..."
4. Records all requests in `assignment_requests` table

### Anti-Spam Measures
- Max 5 assignment requests per day
- 30-minute cooldown between comments on same repo
- Never comments twice on same issue
- Tracks all interaction history in database

---

## 5. Issue State Tracking

### States (in `issue_tracker.go`)
| State | Description |
|-------|-------------|
| `new` | Just discovered |
| `notified` | User was emailed about it |
| `asked_assignment` | Asked to be assigned |
| `assigned` | Successfully assigned to user |
| `in_progress` | User is working on it |
| `pr_submitted` | PR submitted |
| `completed` | PR merged |
| `abandoned` | User gave up |

### Database Schema Updates
```sql
-- Added columns to tracked_issues:
notified_at TIMESTAMP,
assignment_asked_at TIMESTAMP,
has_good_first BOOLEAN,
has_confirmed BOOLEAN,
has_assignee BOOLEAN,
has_pr BOOLEAN
```

---

## 6. Configuration Options

### New Environment Variables
```bash
# Assignment Configuration
ASSIGNMENT_ENABLED=false      # Enable auto-assignment feature
ASSIGNMENT_AUTO_MODE=false    # Auto-assign without prompting
ASSIGNMENT_MAX_DAILY=5        # Max assignment requests per day
ASSIGNMENT_COOLDOWN_MINS=30   # Cooldown between comments

# Mode Selection
MODE=confirmed                # New mode for confirmed good first issues
TARGET_REPO=kubernetes/kubernetes  # Optional: limit to specific repo
```

---

## 7. Usage

### Find Confirmed Good First Issues
```bash
MODE=confirmed ./github-issue-finder

# Or for a specific repo:
MODE=confirmed TARGET_REPO=kubernetes/kubernetes ./github-issue-finder
```

### With Assignment Auto-Mode
```bash
MODE=confirmed ASSIGNMENT_ENABLED=true ASSIGNMENT_AUTO_MODE=true ./github-issue-finder
```

---

## 8. Files Modified/Created

### Created
- `assignment_manager.go` - Handles auto-assignment logic, spam protection

### Modified
- `main.go` - Added `FindConfirmedGoodFirstIssues()`, `ProcessNewIssueNotifications()`, new `confirmed` mode
- `issue_analyzer.go` - Added label checking functions
- `issue_tracker.go` - Added new states and tracking fields
- `enhanced_scorer.go` - Updated scoring priorities
- `anti_spam.go` - Added comment tracking, notification deduplication
- `config.go` - Added assignment configuration
- `.env.example` - Added new configuration options

---

## 9. Current Issues from kubernetes/kubernetes Meeting Criteria

Based on the latest GitHub search for issues with:
- Label: `good first issue` + (`confirmed` OR `triage/accepted`)
- No assignee
- No linked PR
- Open state

### Top Eligible Issues:

1. **#137132** - Add additional test coverage for HPA External Metrics framework
   - Labels: good first issue, triage/accepted, sig/autoscaling
   - Status: Open, No assignee, No PR
   - Created: Feb 19, 2026

2. **#137058** - Cleanup: Replace manual port-forward implementation with kubectl PortForwardOptions
   - Labels: good first issue, triage/accepted, kind/cleanup
   - Status: Open, No assignee, No PR
   - Created: Feb 16, 2026

3. **#135487** - resource.MustParse fails to parse quantities near math.MaxInt64
   - Labels: good first issue, triage/accepted, kind/bug
   - Status: Open, No assignee, No PR

4. **#127826** - device manager: potential Double-Locking of Mutex
   - Labels: good first issue, priority/important-longterm
   - Status: Open, No assignee, No PR

5. **#126379** - add and use alternative APIs which support contextual logging
   - Labels: good first issue, wg/structured-logging
   - Status: Open, No assignee, No PR

> Note: Run `MODE=confirmed TARGET_REPO=kubernetes/kubernetes ./github-issue-finder` for live results
