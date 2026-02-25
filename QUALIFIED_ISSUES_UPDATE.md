# GitHub Issue Finder - Qualified Issues Update

## Summary of Changes

### 1. New Search Strategy

**INCLUDE (Qualified Issues):**
- Bugs labeled `confirmed`, `triage/accepted`, `approved`, or with maintainer confirmation
- Features labeled `approved` or `planned`
- Issues labeled `help wanted` with clear scope
- Issues with clear description and acceptance criteria
- Issues where maintainers have said "feel free to submit a PR"
- Issues with `priority/P1`, `priority/P2`, or `priority/P3` labels

**EXCLUDE (Not Qualified):**
- Questions (label: `question`, `support`, `documentation` only)
- Discussions that aren't actionable
- Issues marked `wontfix`, `duplicate`, `invalid`
- Issues marked `stale` without activity
- Issues without maintainer response

### 2. New Scoring System

```go
type QualifiedIssueScore struct {
    // Impact (0-40 points weight)
    ProjectStars         float64  // Popular projects = more impact
    IsBug                float64  // Real bugs > features for learning
    HasClearReproduction float64  // Clear steps = easier to fix
    
    // Approval (0-30 points weight)
    MaintainerApproved   float64  // "triage/accepted", "approved", "confirmed"
    HasAcceptanceCriteria float64 // Clear definition of done
    Priority             float64  // P1 > P2 > P3
    
    // Accessibility (0-20 points weight)
    NoAssignee           float64  // Available to work
    NoOpenPR             float64  // No one working on it
    LowComments          float64  // Less competition
    
    // Quality (0-10 points weight)
    GoodDescription      float64  // Well documented
    HasCodeSnippets      float64  // Easier to understand
    RecentActivity       float64  // Not abandoned
}
```

### 3. Anti-Spam Notification System

```go
type NotificationTracker struct {
    IssueURL       string
    FirstNotified  time.Time
    LastNotified   time.Time
    NotifyCount    int
    UserResponded  bool
}

// Rules:
// - Never notify about same issue more than once
// - Max 5 notifications per hour
// - Max 20 notifications per day
// - Digest mode: batch notifications every 6 hours
```

### 4. Command Structure

```bash
# Find qualified issues (main command)
./github-issue-finder find [--score-min 0.6] [--category kubernetes]

# Find issues by type
./github-issue-finder bugs      # Bug reports only
./github-issue-finder features  # Feature requests only

# Notification management
./github-issue-finder notify --email --local --score-min 0.7

# Check your assigned issues
./github-issue-finder mine

# Stats
./github-issue-finder stats
```

### 5. Configuration Updates

```bash
# Qualified Issue Settings
QUALIFIED_MIN_SCORE=0.6
QUALIFIED_TYPES=bug,feature,enhancement
QUALIFIED_EXCLUDE_LABELS=question,support,wontfix,duplicate,invalid

# Notification Settings
NOTIFY_LOCAL=true
NOTIFY_EMAIL=true
NOTIFY_EMAIL_MIN_SCORE=0.7
NOTIFY_MAX_PER_HOUR=5
NOTIFY_MAX_PER_DAY=20
NOTIFY_DIGEST_MODE=false
NOTIFY_DIGEST_INTERVAL=6h

# Anti-Spam
NEVER_NOTIFY_TWICE=true
CHECK_USER_COMMENTS=true
CHECK_USER_PRS=true
```

### 6. Current Qualified Issues Found from GitHub

**High-Impact Bugs with triage/accepted:**
| Issue | Project | Labels | Score |
|-------|---------|--------|-------|
| CapacityReservation cache possibly marked as unavailable | aws/karpenter-provider-aws | bug, good-first-issue, triage/accepted | High |
| Trivial bottlerocket NodePool filters out all instance types | aws/karpenter-provider-aws | bug, triage/accepted | High |
| Karpenter incorrectly calculates Node's memory allocable | aws/karpenter-provider-aws | bug, help-wanted, triage/accepted | High |

**Confirmed Bugs:**
| Issue | Project | Labels |
|-------|---------|--------|
| API pagination returns duplicate records | demo-product | bug, confirmed |
| CSV export spinner hangs on dashboards | demo-product | bug, confirmed, priority: high |

**Help-Wanted Bugs:**
| Issue | Project | Labels |
|-------|---------|--------|
| Crash when building from source on macos | system-bridge | bug, help-wanted |
| Validate RunInstancesAuthCheck Fails | aws/karpenter-provider-aws | bug, help-wanted, triage/accepted |

### 7. Files Modified

1. **qualified_issue_finder.go** (NEW) - Core qualified issue detection and scoring
2. **config.go** - Added QualifiedIssueConfig and NotificationConfig
3. **commands.go** - New commands: bugs, features, notify, mine
4. **display.go** - New display format for qualified issues
5. **email_templates.go** - New HTML templates for qualified issues
6. **local_notifier.go** - Added SendQualifiedIssueEmail method
7. **.env.example** - New configuration options

### 8. How Scoring Works

**Impact (40% weight):**
- Project stars: 100k+ = 1.0, 50k+ = 0.9, 10k+ = 0.8
- Is bug: 1.0 (bugs are better for learning)
- Has reproduction steps: 1.0

**Approval (30% weight):**
- Maintainer approved: 1.0
- Has acceptance criteria: 1.0
- Priority P1/P2/P3: 1.0/0.8/0.6

**Accessibility (20% weight):**
- No assignee: 1.0
- No open PR: 1.0
- Low comments (≤2): 1.0

**Quality (10% weight):**
- Good description: 0.0-1.0
- Code snippets: 1.0
- Recent activity: 1.0

### 9. How Notifications Work

**Local Notification:**
- Desktop notification (using notify-send on Linux)
- Log to file with timestamp
- Show in CLI with colors

**Email Notification:**
- Send for HIGH-scoring issues only (score > 0.7)
- HTML email with:
  - Issue title and link
  - Project info (stars, language)
  - Score breakdown
  - Why it's a good fit
  - Quick action buttons (Open Issue, Clone Repo)
- Daily digest option

**Anti-Spam Rules:**
- Never notify about same issue twice
- Never notify if user already commented
- Max 5 per hour, 20 per day
- Digest mode available

### 10. Example Output

```
================================================================================
🎯 QUALIFIED ISSUES - Resume-Worthy Opportunities
================================================================================

🔥 HIGH IMPACT (Score >= 0.8)
--------------------------------------------------------------------------------
✅ [1] Fix memory leak in pod eviction controller (Score: 0.87)
   📦 kubernetes/kubernetes (110k ⭐) | Go | Priority: P1
   📝 Type: Bug | Labels: triage/accepted, help wanted
   👤 Assignee: None | PRs: 0 | Comments: 3
   🔗 https://github.com/kubernetes/kubernetes/issues/137XXX
   
   Why it's good:
   • Popular project (110k stars)
   • Confirmed and triaged by maintainers
   • Priority: P1
   • No assignee - available to work on
   • Real bug fix - great for resume
--------------------------------------------------------------------------------

📊 SUMMARY
   High Impact: 3 | Medium: 5 | Low: 2
   Total Qualified: 10

🔔 NOTIFICATIONS SENT
   Email: 3 (score > 0.7)
   Local: 10 (all qualified)
================================================================================
```

## Commands to Run

```bash
# Build
cd /home/mehrdad/k8s-investigation/github-issue-finder
go build -o github-issue-finder .

# Find qualified issues
./github-issue-finder find

# Find bugs only
./github-issue-finder bugs

# Find features only
./github-issue-finder features

# Find and notify (with email for high scores)
./github-issue-finder notify --email --score-min 0.7

# Check your assigned issues
./github-issue-finder mine

# View statistics
./github-issue-finder stats
```
