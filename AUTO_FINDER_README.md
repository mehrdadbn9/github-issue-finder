# GitHub Issue Finder - Auto Mode

## Files Created/Modified

### New Files
- `file_storage.go` - File-based storage backend (fallback when no database)
- `config.yaml` - YAML configuration file

### Modified Files
- `auto_finder.go` - Added file storage support, dual-mode (DB/file) operation
- `commands.go` - Updated history command for new data structures

## Usage Examples

### Basic Commands

```bash
# Start automated mode (continuous searching)
./github-issue-finder start

# One-time search for issues
./github-issue-finder search

# Search with minimum score filter
./github-issue-finder search --min-score 0.8

# Show current status
./github-issue-finder status

# Enable auto-comment mode
./github-issue-finder enable

# Disable auto-comment mode
./github-issue-finder disable

# Show comment history
./github-issue-finder history

# Show comment history with limit
./github-issue-finder history 50

# Show configuration
./github-issue-finder config

# Manual comment on an issue
./github-issue-finder comment kubernetes-sigs/kubespray 12345
```

## Storage Modes

### Database Mode (PostgreSQL)
Set `DB_CONNECTION_STRING` environment variable:
```bash
export DB_CONNECTION_STRING="host=localhost user=postgres password=postgres dbname=issue_finder sslmode=disable port=5432"
```

### File-Based Mode (No Database Required)
If no database is configured, the tool automatically uses JSON files:
- `~/.github-issue-finder/history.json` - Comment history
- `~/.github-issue-finder/daily_limits.json` - Daily tracking
- `~/.github-issue-finder/found_issues.json` - Found issues cache
- `~/.github-issue-finder/status.json` - Current status
- `~/.github-issue-finder/config.json` - Runtime config

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GITHUB_TOKEN` | (required) | GitHub personal access token |
| `AUTO_FINDER_ENABLED` | false | Enable auto-finder |
| `AUTO_COMMENT` | false | Enable auto-commenting |
| `MAX_COMMENTS_PER_DAY` | 3 | Maximum comments per day |
| `MAX_COMMENTS_PER_REPO` | 1 | Maximum comments per repo per day |
| `MIN_SCORE_TO_COMMENT` | 0.75 | Minimum score threshold |
| `MIN_HOURS_BETWEEN_COMMENTS` | 2 | Hours between comments |
| `NOTIFY_ON_COMMENT` | true | Notify when comment posted |
| `NOTIFY_ON_FIND` | true | Notify when issue found |
| `DB_CONNECTION_STRING` | (none) | PostgreSQL connection (optional) |

### Config File (config.yaml)

Located in the project directory or `~/.github-issue-finder/config.yaml`:

```yaml
auto_finder:
  enabled: true
  auto_comment: true
  max_comments_per_day: 3
  max_comments_per_repo: 1
  min_score_to_comment: 0.75
  min_hours_between_comments: 2

repos:
  included:
    - owner: kubernetes-sigs
      name: kubespray
      category: kubernetes
      priority: 1
    - owner: cilium
      name: cilium
      category: networking
      priority: 1
    - owner: kyverno
      name: kyverno
      category: security
      priority: 1

scoring:
  stars_weight: 0.15
  bug_severity_weight: 0.15
  maintainer_confirmed_weight: 0.20
  no_assignee_weight: 0.15
  no_pr_weight: 0.10
  clear_description_weight: 0.10
  recent_activity_weight: 0.05

notifications:
  email: true
  local: true
  telegram: false
```

## How to Enable/Disable Automation

### Enable
```bash
# Via environment variable
export AUTO_FINDER_ENABLED=true
export AUTO_COMMENT=true

# Or via command
./github-issue-finder enable
```

### Disable
```bash
# Via environment variable
export AUTO_FINDER_ENABLED=false

# Or via command
./github-issue-finder disable
```

## Today's Comments (Already Posted)

The following comments were posted today and should NOT be repeated:
- kubernetes-sigs/kubespray #13031
- cilium/cilium #44436
- kyverno/kyverno #15309

The tool tracks these internally to avoid duplicate comments.

## Status Display Example

```
╔══════════════════════════════════════════════════════════╗
║           GITHUB ISSUE FINDER - STATUS                   ║
╠══════════════════════════════════════════════════════════╣
║ Auto Mode: ✅ Enabled                                    ║
║ Auto Comment: Enabled                                    ║
╠══════════════════════════════════════════════════════════╣
║ Today's Comments: 3/3 (Limit reached)                    ║
║ Repos Commented: kubespray, cilium, kyverno              ║
╠══════════════════════════════════════════════════════════╣
║ Next Available: Tomorrow                                 ║
║ Pending Issues: 5 (score > 0.75)                         ║
║ Min Score Threshold: 0.75                                ║
╚══════════════════════════════════════════════════════════╝
```
