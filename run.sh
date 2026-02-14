#!/bin/bash

# Run GitHub Issue Finder with Docker Compose

echo "🚀 Starting GitHub Issue Finder..."

# Check if .env file exists
if [ ! -f .env ]; then
    echo "❌ .env file not found"
    echo "Please copy .env.example to .env and configure it"
    exit 1
fi

# Start services
docker-compose up -d

echo ""
echo "✅ GitHub Issue Finder is now running!"
echo ""
echo "View logs with:"
echo "  docker-compose logs -f github-issue-finder"
echo ""
echo "Stop with:"
echo "  docker-compose down"
echo ""
