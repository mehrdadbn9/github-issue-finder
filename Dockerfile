FROM hub.hamdocker.ir/golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata

ENV GOPROXY=https://proxy.golang.org,direct
ENV GO111MODULE=on
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY *.go ./
RUN GOOS=linux GOARCH=amd64 go build -ldflags="-w -s -X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" -o github-issue-finder .

FROM hub.hamdocker.ir/alpine:3.19

RUN apk --no-cache add ca-certificates tzdata dumb-init && \
    addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -D appuser

WORKDIR /app

RUN mkdir -p /app/logs /app/data && \
    chown -R appuser:appgroup /app

COPY --from=builder --chown=appuser:appgroup /app/github-issue-finder .

ENV LOG_LEVEL=info
ENV LOG_FORMAT=text
ENV TZ=UTC

USER appuser

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD pgrep github-issue-finder || exit 1

ENTRYPOINT ["/usr/bin/dumb-init", "--"]
CMD ["./github-issue-finder"]