.PHONY: build run test test-coverage clean docker-build docker-run lint fmt

APP_NAME := github-issue-finder
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GO_VERSION := $(shell go version | awk '{print $$3}')

LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

build:
	@echo "Building $(APP_NAME)..."
	go build $(LDFLAGS) -o bin/$(APP_NAME) .
	@echo "Built: bin/$(APP_NAME)"

run:
	go run .

test:
	go test -v -race ./...

test-coverage:
	@echo "Running tests with coverage..."
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
	@go tool cover -func=coverage.out | tail -1

test-coverage-short:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

coverage-view:
	go tool cover -html=coverage.out

benchmark:
	go test -bench=. -benchmem ./...

clean:
	rm -rf bin/
	rm -f coverage.out coverage.html
	go clean

docker-build:
	docker build -t $(APP_NAME):$(VERSION) -t $(APP_NAME):latest .

docker-run:
	docker-compose up -d

docker-stop:
	docker-compose down

docker-logs:
	docker-compose logs -f

lint:
	golangci-lint run ./...

fmt:
	go fmt ./...
	goimports -w .

vet:
	go vet ./...

staticcheck:
	staticcheck ./...

check: fmt vet lint test

install-deps:
	go mod download
	go mod tidy

update-deps:
	go get -u ./...
	go mod tidy

db-reset:
	docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "TRUNCATE TABLE seen_issues; TRUNCATE TABLE issue_history; TRUNCATE TABLE comment_history;"

db-stats:
	docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "SELECT 'seen_issues' as table_name, COUNT(*) as count FROM seen_issues UNION ALL SELECT 'issue_history', COUNT(*) FROM issue_history UNION ALL SELECT 'comment_history', COUNT(*) FROM comment_history;"

comment-stats:
	docker exec issue-finder-postgres psql -U postgres -d issue_finder -c "SELECT project_name, COUNT(*) as comments, MAX(commented_at) as last_comment FROM comment_history GROUP BY project_name ORDER BY comments DESC;"

scheduled-push:
	./scripts/scheduled_push.sh

version:
	@echo "App: $(APP_NAME)"
	@echo "Version: $(VERSION)"
	@echo "Go: $(GO_VERSION)"
	@echo "Built: $(BUILD_TIME)"

help:
	@echo "Available targets:"
	@echo "  build           - Build the application"
	@echo "  run             - Run the application"
	@echo "  test            - Run tests"
	@echo "  test-coverage   - Run tests with coverage report"
	@echo "  benchmark       - Run benchmarks"
	@echo "  clean           - Clean build artifacts"
	@echo "  docker-build    - Build Docker image"
	@echo "  docker-run      - Run Docker container"
	@echo "  lint            - Run linter"
	@echo "  fmt             - Format code"
	@echo "  db-reset        - Reset database tables"
	@echo "  db-stats        - Show database statistics"
	@echo "  comment-stats   - Show comment statistics"
	@echo "  check           - Run fmt, vet, lint, test"
	@echo "  version         - Show version info"
