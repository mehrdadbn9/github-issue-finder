# GitHub Issue Finder for Go DevOps Projects

A Telegram bot that finds and alerts you about good learning opportunities (issues) in popular Go DevOps projects.

## Features

- Monitors 500+ popular Go DevOps projects
- Scores issues based on multiple factors:
  - Project star count
  - Issue recency
  - Number of comments
  - Labels (good first issue, help wanted, etc.)
  - Difficulty level
  - Description quality
  - Project activity level
  - Maintainer responsiveness
  - Contributor friendliness
  - Weekend/weekday timing
- Sends Telegram alerts for high-scoring issues
- Email notifications via SMTP with beautiful HTML templates
- Persistent storage to track already notified issues
- Configurable check intervals

### New Features (v2.0)

- **Issue Tracking**: Track issues you're working on with statuses (interested, assigned, in_progress, completed, abandoned)
- **Anti-Spam Protection**: Rate limiting, cooldown periods, daily/hourly limits, and digest mode
- **Enhanced Scoring**: Better prioritization with configurable weights
- **CLI Commands**: Full command-line interface for managing tracked issues
- **Email System**: SMTP support with HTML templates, rate limiting, and digest mode
- **Partitioned Display**: Issues displayed in sections sorted by score
- **Assignment Management**: Automatic assignment request handling

## CLI Commands

```bash
# Find new issues
github-issue-finder find

# Find good first issues
github-issue-finder good-first

# Find actionable issues
github-issue-finder actionable

# Find confirmed good first issues (ready for assignment)
github-issue-finder confirmed

# Track an issue you're working on
github-issue-finder track --url https://github.com/kubernetes/kubernetes/issues/123456 \
  --title "Fix bug" --org kubernetes --repo kubernetes --number 123456 \
  --status interested --notes "Good learning opportunity"

# Update issue status
github-issue-finder update --url https://github.com/kubernetes/kubernetes/issues/123456 \
  --status in_progress --notes "Started working on this"

# List tracked issues
github-issue-finder list --all
github-issue-finder list --status in_progress

# Check issue status
github-issue-finder status --url https://github.com/kubernetes/kubernetes/issues/123456

# View statistics
github-issue-finder stats

# Daily digest
github-issue-finder digest

# Test email configuration
github-issue-finder email-test
```

## MCP (Model Context Protocol) Integration

The GitHub Issue Finder supports MCP (Model Context Protocol), enabling seamless integration with AI assistants like Claude Desktop. MCP allows AI assistants to access project features as tools, enabling AI-enhanced comment generation, issue analysis, and automated workflows.

### Benefits

- **AI Assistant Integration**: Connect directly to Claude Desktop and other MCP-compatible AI assistants
- **AI-Accessible Tools**: Expose all project features as tools that AI assistants can invoke
- **Enhanced Workflows**: Enable AI-enhanced comment generation and intelligent issue analysis
- **Automated Discovery**: Let AI assistants find and prioritize issues based on your preferences
- **Smart Tracking**: AI can manage your tracked issues and suggest next steps

### MCP Server Mode

Run GitHub Issue Finder as an MCP server to connect with AI assistants:

```bash
# Run as MCP stdio server (for Claude Desktop)
./github-issue-finder mcp

# Run as MCP HTTP server
./github-issue-finder mcp-http --port 8080

# List available MCP tools
./github-issue-finder mcp-list-tools

# Test MCP server
./github-issue-finder mcp-test
```

### Available MCP Tools

The MCP server exposes 12 tools for AI assistants:

| Tool | Description |
|------|-------------|
| `find_issues` | Search for issues matching custom criteria |
| `find_good_first_issues` | Find beginner-friendly issues with good labels |
| `find_confirmed_issues` | Find triage-confirmed issues ready for assignment |
| `get_issue_score` | Get detailed scoring breakdown for an issue |
| `track_issue` | Add an issue to your tracked list |
| `list_tracked_issues` | View all issues you're tracking |
| `update_issue_status` | Update status of a tracked issue |
| `generate_comment` | Generate a professional comment for an issue |
| `search_repos` | Search configured repositories |
| `get_stats` | Get overall statistics and metrics |
| `get_issue_details` | Retrieve detailed information about an issue |
| `analyze_issue` | Perform deep analysis on an issue |

### MCP Resources

The MCP server exposes the following resources:

| Resource | Description |
|----------|-------------|
| `tracked://` | All tracked issues with statuses |
| `config://` | Current configuration settings |
| `repos://` | Configured repositories list |
| `issue://{owner}/{repo}/{number}` | Individual issue template |

### MCP Prompts

Pre-built prompts for common AI workflows:

| Prompt | Description |
|--------|-------------|
| `find_resume_worthy_issues` | Find issues that would look great on your resume |
| `analyze_and_suggest` | Analyze an issue and suggest next steps |
| `create_contribution_plan` | Create a structured contribution plan |
| `generate_issue_comment` | Generate a professional issue comment |

### Claude Desktop Configuration

Add the following to your Claude Desktop configuration file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows**: `%APPDATA%\Claude\claude_desktop_config.json`
**Linux**: `~/.config/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "github-issue-finder": {
      "command": "/path/to/github-issue-finder",
      "args": ["mcp"]
    }
  }
}
```

For HTTP mode:

```json
{
  "mcpServers": {
    "github-issue-finder": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

### AI Enhancement Configuration

Configure the MCP client for AI-enhanced features in your `config.yaml`:

```yaml
mcp:
  client:
    enabled: true
    servers:
      - name: "claude"
        command: "claude-mcp-server"
        args: []
      - name: "openai"
        url: "http://localhost:8081/mcp"
    timeout: 30s
    retry_count: 3
```

### MCP Configuration Reference

Full MCP configuration options:

```yaml
mcp:
  server:
    enabled: true
    type: stdio              # stdio or http
    port: 8080               # HTTP port (if type: http)
    host: "localhost"        # HTTP host
    
  client:
    enabled: true
    timeout: 30s
    retry_count: 3
    retry_delay: 1s
    
    servers:
      - name: "local"
        command: "/path/to/github-issue-finder"
        args: ["mcp"]
        env:
          GITHUB_TOKEN: "${GITHUB_TOKEN}"
          
  tools:
    enabled:
      - find_issues
      - find_good_first_issues
      - find_confirmed_issues
      - get_issue_score
      - track_issue
      - list_tracked_issues
      - update_issue_status
      - generate_comment
      - search_repos
      - get_stats
      - get_issue_details
      - analyze_issue
      
  resources:
    enabled: true
    cache_ttl: 5m
    
  prompts:
    enabled: true
    custom_prompts_dir: "./prompts"
```

### Environment Variables for MCP

```bash
# MCP Server Settings
MCP_ENABLED=true
MCP_SERVER_TYPE=stdio          # stdio or http
MCP_HTTP_PORT=8080
MCP_HTTP_HOST=localhost

# MCP Client Settings
MCP_CLIENT_ENABLED=true
MCP_CLIENT_TIMEOUT=30s
MCP_CLIENT_RETRY_COUNT=3
```

## Email Configuration

### Basic Email Setup

Set the following environment variables:

```bash
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=your-email@gmail.com
SMTP_PASSWORD=your-app-password
FROM_EMAIL=your-email@gmail.com
TO_EMAIL=recipient@example.com
```

### Email Modes

- `EMAIL_MODE=instant` - Send emails immediately when issues are found
- `EMAIL_MODE=digest` - Send daily digest at configured time

### Email Rate Limiting

- `MAX_EMAILS_PER_HOUR=10` - Maximum emails per hour
- `MAX_EMAILS_PER_DAY=50` - Maximum emails per day

### Email Templates

Beautiful HTML email templates are available for:
- New issue notification (with score breakdown)
- Daily digest
- Assignment confirmation
- Assignment request sent

## Scoring Configuration

All scoring weights can be customized via environment variables:

```bash
# Scoring Weights (0.0 - 1.0)
SCORING_STAR_WEIGHT=0.08              # Project popularity
SCORING_COMMENT_WEIGHT=0.15           # Less competition bonus
SCORING_RECENCY_WEIGHT=0.15           # Newer issues
SCORING_LABEL_WEIGHT=0.20             # Good labels (GFI, help wanted)
SCORING_DIFFICULTY_WEIGHT=0.12        # Simpler issues
SCORING_DESCRIPTION_WEIGHT=0.10       # Clear description
SCORING_ACTIVITY_WEIGHT=0.10          # Active project
SCORING_MAINTAINER_WEIGHT=0.10        # Responsive maintainers

# Bonus Scores
SCORING_CONTRIBUTOR_FRIENDLY_BONUS=0.15  # Beginner-friendly labels
SCORING_WEEKEND_BONUS=0.05               # Issues opened on weekends
SCORING_MAX_SCORE=1.5                    # Maximum possible score
```

## Anti-Spam Configuration

```bash
# Notification Limits
MAX_NOTIFICATIONS_PER_HOUR=10
DAILY_NOTIFICATION_LIMIT=30
MAX_NOTIFICATIONS_PER_PROJECT=2
NOTIFICATION_COOLDOWN_HOURS=24

# Comment Limits
MAX_COMMENTS_PER_DAY=5

# GitHub API Limits
MAX_GITHUB_CALLS_PER_HOUR=4000

# Digest Mode
DIGEST_MODE=false
DIGEST_TIME=09:00
```

## Assignment Configuration

```bash
# Enable automatic assignment requests
ASSIGNMENT_ENABLED=true
ASSIGNMENT_AUTO_MODE=false    # Set true for automatic without prompts
ASSIGNMENT_MAX_DAILY=5
ASSIGNMENT_COOLDOWN_MINS=30
ASSIGNMENT_CHECK_ELIGIBILITY=true
ASSIGNMENT_AUTO_COMMENT=false
```

## Display Configuration

```bash
DISPLAY_MODE=partitioned      # Options: partitioned, simple, json
DISPLAY_MAX_GOOD_FIRST=15     # Max good first issues to show
DISPLAY_MAX_OTHER=10          # Max other issues to show
DISPLAY_MAX_ASSIGNED=10       # Max assigned issues to show
DISPLAY_SHOW_SCORE_BREAKDOWN=true
```

## Supported Projects & Categories

### 🔧 Kubernetes Tools (100+ projects)
- kubernetes/kubernetes (105k★)
- helm/helm (25k★)
- cilium/cilium (18k★)
- rancher/rancher (22k★)
- And 100+ more...

### 📈 Monitoring Tools (100+ projects)
- prometheus/prometheus (53k★)
- grafana/grafana (58k★)
- jaegertracing/jaeger (19k★)
- thanos-io/thanos (12k★)
- And 100+ more...

### 🚀 CI/CD Tools (80+ projects)
- argoproj/argo-cd (15k★)
- drone/drone (28k★)
- tektoncd/pipeline (8k★)
- fluxcd/flux2 (6k★)
- And 80+ more...

### 🔐 Security Tools (30+ projects)
- aquasecurity/trivy (21k★)
- kyverno/kyverno (5k★)
- falcosecurity/falco (5k★)
- And 30+ more...

### 🤖 ML/AI Projects (50+ projects)
- tensorflow/tensorflow (185k★)
- pytorch/pytorch (85k★)
- huggingface/transformers (140k★)
- And 50+ more...

## Setup

### Prerequisites

1. Go 1.21 or higher
2. PostgreSQL database
3. GitHub Personal Access Token
4. Telegram Bot Token (optional)
5. SMTP credentials (optional)

### Installation

1. Clone the repository:
```bash
git clone <your-repo-url>
cd github-issue-finder
```

2. Install dependencies:
```bash
go mod download
```

3. Set up environment variables:
```bash
cp .env.example .env
# Edit .env with your credentials
```

4. Create PostgreSQL database:
```sql
CREATE DATABASE issue_finder;
```

5. Run the application:
```bash
make run
# or
go run main.go
```

### Makefile Commands

```bash
make build          # Build binary
make run            # Run the application
make test           # Run tests
make test-coverage  # Run tests with coverage
make email-test     # Test email configuration
make digest         # Show daily digest
make lint           # Run linter
make fmt            # Format code
make db-stats       # Show database statistics
make help           # Show all commands
```

## Issue Scoring

Issues are scored based on configurable weights:

| Factor | Weight | Description |
|--------|--------|-------------|
| Stars | 8% | Project popularity |
| Comments | 15% | Less competition = higher score |
| Recency | 15% | Newer issues score higher |
| Labels | 20% | Good first issue, help wanted, etc. |
| Difficulty | 12% | Simpler issues score higher |
| Description | 10% | Clear steps, code examples |
| Activity | 10% | Active project |
| Maintainer | 10% | Responsive maintainers |

Score ranges:
- 🔥 0.8+: Excellent learning opportunity
- ⭐ 0.6-0.8: Good opportunity
- ✨ Below 0.6: Worth considering

### Bonus Factors

- Good first issue label: +0.30
- Confirmed/triage-accepted: +0.35
- Help wanted label: +0.10
- No assignee: +0.10
- Documentation: +0.15
- CNCF project: +0.10
- TLS/Security related: +0.10
- Beginner-friendly: +0.15
- Weekend posting: +0.05

### Penalty Factors

- Cloud provider specific: -0.50
- Needs triage: -0.15
- Blocked/waiting: -0.20
- Has assignee: -0.25
- Has linked PR: -0.30
- Wontfix/invalid: -0.50

## Database Schema

The tool uses PostgreSQL to track:

- **seen_issues**: Issues already discovered
- **issue_history**: All discovered issues with scores
- **tracked_issues**: Issues you're working on
- **notification_log**: Notification history
- **comment_log**: Comment history
- **assignment_requests**: Assignment request history

## Running as a Service

### systemd

Create `/etc/systemd/system/github-issue-finder.service`:
```
[Unit]
Description=GitHub Issue Finder
After=network.target

[Service]
Type=simple
User=your-user
WorkingDirectory=/path/to/github-issue-finder
EnvironmentFile=/path/to/.env
ExecStart=/usr/local/bin/github-issue-finder
Restart=always

[Install]
WantedBy=multi-user.target
```

### Docker

```bash
docker build -t github-issue-finder .
docker run -d \
  --name github-issue-finder \
  --env-file .env \
  --link postgres:postgres \
  github-issue-finder
```

## Development

### Running tests
```bash
make test
make test-coverage
```

### Building
```bash
make build
```

### Linting
```bash
make lint
make fmt
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License
