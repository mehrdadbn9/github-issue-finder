#!/bin/bash

# GitHub Issue Finder Setup Script

echo "🚀 GitHub Issue Finder Setup"
echo "=============================="
echo ""

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    echo "❌ Docker is not installed. Please install Docker first."
    exit 1
fi

# Check if PostgreSQL is installed
if ! command -v psql &> /dev/null; then
    echo "❌ PostgreSQL is not installed. Please install PostgreSQL first."
    exit 1
fi

echo "✅ Prerequisites check passed"
echo ""

# Check environment variables
if [ -z "$GITHUB_TOKEN" ]; then
    echo "❌ GITHUB_TOKEN environment variable is not set"
    echo "Please set your GitHub Personal Access Token:"
    echo "export GITHUB_TOKEN=your_github_token_here"
    exit 1
fi

if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    echo "❌ TELEGRAM_BOT_TOKEN environment variable is not set"
    echo "Please set your Telegram Bot Token:"
    echo "export TELEGRAM_BOT_TOKEN=your_telegram_bot_token_here"
    exit 1
fi

echo "✅ Environment variables check passed"
echo ""

# Create database if it doesn't exist
echo "📦 Creating database..."
createdb issue_finder 2>/dev/null || echo "Database already exists or couldn't create (this is OK)"
echo ""

# Run migrations
echo "🗄️  Running database migrations..."
# The application will handle schema creation on startup
echo ""

echo "✅ Setup complete!"
echo ""
echo "You can now run the application with:"
echo "  ./github-issue-finder"
echo ""
echo "Or with Docker:"
echo "  docker-compose up -d"
echo ""
echo "Application will:"
echo "- Check 200+ Go DevOps projects for good learning issues"
echo "- Score and rank issues based on multiple factors"
echo "- Send Telegram alerts for high-scoring issues"
echo "- Run every 3600 seconds (1 hour)"
