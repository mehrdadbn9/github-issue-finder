package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunMCPHTTPServer(port int) error {
	mcpServer, err := NewMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}

	srv := mcpServer.CreateServer()

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `GitHub Issue Finder MCP Server

Endpoints:
  /mcp     - MCP protocol endpoint (POST requests)
  /health  - Health check endpoint

Available MCP Tools:
  - find_issues: Find issues based on various criteria
  - find_good_first_issues: Find beginner-friendly issues
  - find_confirmed_issues: Find confirmed issues ready for assignment
  - get_issue_score: Get detailed score breakdown
  - track_issue: Start tracking an issue
  - list_tracked_issues: List tracked issues
  - update_issue_status: Update issue status
  - generate_comment: Generate smart comment
  - search_repos: Search repositories
  - get_stats: Get statistics
  - get_issue_details: Get issue details
  - analyze_issue: Analyze issue for contribution potential
`)
		} else {
			http.NotFound(w, r)
		}
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("MCP HTTP server starting on port %d", port)
	log.Printf("MCP endpoint: http://localhost:%d/mcp", port)
	log.Printf("Health check: http://localhost:%d/health", port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	return server.Shutdown(shutdownCtx)
}
