#!/bin/bash

echo "🧪 GitHub Issue Finder - Quick Test Script"
echo "========================================="

# Check if tokens are set
if [ -z "$GITHUB_TOKEN" ]; then
    echo "❌ GITHUB_TOKEN not set!"
    echo "Please set: export GITHUB_TOKEN=your_token"
    exit 1
fi

if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    echo "❌ TELEGRAM_BOT_TOKEN not set!"
    echo "Please set: export TELEGRAM_BOT_TOKEN=your_token"
    exit 1
fi

echo "✅ Tokens found!"
echo ""

# Start services
echo "🚀 Starting Docker Compose services..."
docker compose up -d

echo "⏳ Waiting for services to be ready..."
sleep 5

# Check status
echo ""
echo "📊 Service Status:"
echo "=================="
docker compose ps

echo ""
echo "⏳ Waiting for PostgreSQL to be ready..."
sleep 10

# Check database
echo ""
echo "🗄️  Testing Database Connection:"
echo "================================"
docker exec issue-finder-postgres pg_isready -U postgres || {
    echo "❌ PostgreSQL not ready yet"
    docker compose logs postgres
    exit 1
}

echo "✅ PostgreSQL is ready!"

# Check tables
echo ""
echo "📋 Database Tables:"
echo "=================="
docker exec -it issue-finder-postgres psql -U postgres -d issue_finder -c "\dt"

# Check logs
echo ""
echo "📋 Application Logs (First 50 lines):"
echo "======================================"
docker compose logs github-issue-finder --tail=50

echo ""
echo "✅ Setup Complete!"
echo ""
echo "📋 Next Steps:"
echo "  1. Check your Telegram for messages"
echo "  2. View logs: docker compose logs -f github-issue-finder"
echo "  3. Check database: docker exec -it issue-finder-postgres psql -U postgres -d issue_finder"
echo "  4. Stop: docker compose down"
echo ""
