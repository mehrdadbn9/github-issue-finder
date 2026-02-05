# GitHub Issue Finder for Go DevOps Projects

A Telegram bot that finds and alerts you about good learning opportunities (issues) in popular Go DevOps projects.

## Features

- Monitors 200+ popular Go DevOps projects (Kubernetes, Monitoring, CI/CD, CNCF tools)
- Scores issues based on multiple factors:
  - Project star count
  - Issue recency
  - Number of comments
  - Labels (good first issue, help wanted, etc.)
  - Difficulty level
- Sends Telegram alerts for high-scoring issues
- Persistent storage to track already notified issues
- Configurable check intervals

## Supported Projects

### Kubernetes Tools
- kubernetes/kubernetes (105k★)
- helm/helm (25k★)
- cilium/cilium (18k★)
- k9s (24k★)
- trivy (21k★)
- And 100+ more...

### Monitoring Tools
- prometheus/prometheus (53k★)
- grafana/grafana (58k★)
- jaegertracing/jaeger (19k★)
- thanos-io/thanos (12k★)
- And 50+ more...

### CI/CD Tools
- argoproj/argo-cd (15k★)
- drone/drone (28k★)
- tektoncd/pipeline (8k★)
- And 50+ more...

## Setup

### Prerequisites

1. Go 1.21 or higher
2. PostgreSQL database
3. GitHub Personal Access Token
4. Telegram Bot Token

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
go run main.go
```

### Environment Variables

- `GITHUB_TOKEN`: Your GitHub Personal Access Token (required)
- `TELEGRAM_BOT_TOKEN`: Your Telegram Bot Token (required)
- `TELEGRAM_CHAT_ID`: Your Telegram Chat ID (default: 683539779)
- `DB_CONNECTION_STRING`: PostgreSQL connection string (default: localhost postgres/postgres)
- `CHECK_INTERVAL`: Check interval in seconds (default: 3600)
- `MAX_ISSUES_PER_REPO`: Max issues to fetch per repo (default: 10)

### Getting Telegram Bot Token

1. Talk to @BotFather on Telegram
2. Create a new bot with `/newbot`
3. Copy the bot token

### Getting Telegram Chat ID

1. Talk to @userinfobot on Telegram
2. Get your Chat ID

## Issue Scoring

Issues are scored based on:
- **Stars Factor (15%)**: Higher star count = higher score
- **Comments Factor (20%)**: Fewer comments = higher score (less competition)
- **Recency Factor (20%)**: More recent = higher score
- **Labels Factor (25%)**: "good first issue", "help wanted" = higher score
- **Difficulty Factor (20%)**: Easier issues = higher score

Score ranges:
- 🔥 0.8+: Excellent learning opportunity
- ⭐ 0.6-0.8: Good opportunity
- ✨ Below 0.6: Worth considering

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
Environment="GITHUB_TOKEN=your_token"
Environment="TELEGRAM_BOT_TOKEN=your_token"
Environment="TELEGRAM_CHAT_ID=683539779"
Environment="DB_CONNECTION_STRING=host=localhost user=postgres password=postgres dbname=issue_finder sslmode=disable"
ExecStart=/usr/local/bin/go run /path/to/github-issue-finder/main.go
Restart=always

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl enable github-issue-finder
sudo systemctl start github-issue-finder
```

### Docker

```bash
docker build -t github-issue-finder .
docker run -d \
  --name github-issue-finder \
  -e GITHUB_TOKEN=your_token \
  -e TELEGRAM_BOT_TOKEN=your_token \
  -e TELEGRAM_CHAT_ID=683539779 \
  -e DB_CONNECTION_STRING=host=postgres user=postgres password=postgres dbname=issue_finder sslmode=disable \
  --link postgres:postgres \
  github-issue-finder
```

## Development

### Building
```bash
go build -o github-issue-finder main.go
```

### Running tests
```bash
go test ./...
```

### Adding new projects

Edit `initializeProjects()` in `main.go` to add new projects:

```go
f.projects = append(f.projects, Project{
    Org:      "org-name",
    Name:     "repo-name",
    Category: "Kubernetes",
    Stars:    5000,
})
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License
