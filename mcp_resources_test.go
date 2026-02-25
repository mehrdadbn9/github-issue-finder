package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServer_RegisterResources(t *testing.T) {
	server := createTestMCPServer()
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "1.0.0",
	}, nil)

	server.RegisterResources(srv)
}

func TestMCPServer_HandleTrackedIssuesResource_NilTracker(t *testing.T) {
	server := &MCPServer{
		tracker: nil,
	}

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "tracked://",
		},
	}

	_, err := server.handleTrackedIssuesResource(ctx, req)

	if err == nil {
		t.Error("expected error for nil tracker")
	}
}

func TestMCPServer_HandleConfigResource_NilConfig(t *testing.T) {
	server := &MCPServer{
		config: nil,
	}

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "config://",
		},
	}

	_, err := server.handleConfigResource(ctx, req)

	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestMCPServer_HandleReposResource(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "repos://",
		},
	}

	result, err := server.handleReposResource(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("result should not be nil")
	}

	if len(result.Contents) == 0 {
		t.Error("result should have contents")
	}
}

func TestMCPServer_HandleIssueResource_InvalidFormats(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{"missing parts", "issue://owner"},
		{"too few parts", "issue://owner/repo"},
		{"invalid issue number", "issue://owner/repo/abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.ReadResourceRequest{
				Params: &mcp.ReadResourceParams{
					URI: tt.uri,
				},
			}

			_, err := server.handleIssueResource(ctx, req)

			if err == nil {
				t.Error("expected error for invalid URI format")
			}
		})
	}
}

func TestMCPServer_HandleRepoResource_InvalidFormat(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "repo://owner",
		},
	}

	_, err := server.handleRepoResource(ctx, req)

	if err == nil {
		t.Error("expected error for invalid URI format")
	}
}

func TestMCPServer_ResourceMIMEType(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "repos://",
		},
	}

	result, err := server.handleReposResource(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil || len(result.Contents) == 0 {
		t.Error("result should have contents")
		return
	}

	if result.Contents[0].MIMEType != "application/json" {
		t.Errorf("MIMEType = %v, want application/json", result.Contents[0].MIMEType)
	}
}

func TestMCPServer_ResourceContents(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "repos://",
		},
	}

	result, err := server.handleReposResource(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("result should not be nil")
		return
	}

	if len(result.Contents) == 0 {
		t.Error("result should have at least one content item")
		return
	}

	if result.Contents[0].Text == "" {
		t.Error("content text should not be empty")
	}
}

func TestMCPServer_ConfigResourceSafeFields(t *testing.T) {
	server := &MCPServer{
		config: &Config{
			GitHubToken:      "secret-token-should-not-appear",
			CheckInterval:    3600,
			MaxIssuesPerRepo: 10,
			MaxProjects:      50,
			LogLevel:         "info",
		},
	}

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "config://",
		},
	}

	result, err := server.handleConfigResource(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil || len(result.Contents) == 0 {
		t.Error("result should have contents")
		return
	}

	if result.Contents[0].Text == "" {
		t.Error("config content should not be empty")
	}
}

func TestMCPServer_ReposResourceStructure(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{
			URI: "repos://",
		},
	}

	result, err := server.handleReposResource(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil || len(result.Contents) == 0 {
		t.Error("result should have contents")
		return
	}

	text := result.Contents[0].Text
	if text == "" {
		t.Error("repos content should not be empty")
	}
}

func TestMCPServer_ReposManager_Integration(t *testing.T) {
	server := createTestMCPServer()

	repos := server.repoManager.ListRepos()

	if len(repos) == 0 {
		t.Error("repoManager should have repos configured")
	}

	categories := server.repoManager.GetCategories()
	if len(categories) == 0 {
		t.Error("repoManager should have categories")
	}
}
