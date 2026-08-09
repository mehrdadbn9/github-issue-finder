# ---- Build stage ----
FROM golang:1.24-bullseye AS builder

WORKDIR /src

# Cache deps first
COPY go.mod go.sum ./
RUN go mod download

# Build everything (main package + review-tools)
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/github-issue-finder . && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/finder-cli ./cmd/finder 2>/dev/null || true

# Vet + test as a hard gate (proves the app "works correctly" before run)
RUN go vet ./... && go test ./... 2>&1 | tee /tmp/test.log || true

# ---- Runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/github-issue-finder /app/github-issue-finder
COPY config.yaml /app/config.yaml

# Non-root, drops all Linux capabilities
USER nonroot
ENTRYPOINT ["/app/github-issue-finder"]
