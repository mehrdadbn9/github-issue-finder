package main

import (
	"context"
	"testing"
	"time"
)

func TestNewMCPClient(t *testing.T) {
	tests := []struct {
		name   string
		config *MCPRuntimeClientConfig
	}{
		{
			name:   "nil config uses defaults",
			config: nil,
		},
		{
			name: "custom config",
			config: &MCPRuntimeClientConfig{
				DefaultTimeout:    60 * time.Second,
				DefaultRetries:    5,
				DefaultRetryDelay: 2 * time.Second,
				EnableFallback:    false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewMCPClient(tt.config)

			if client == nil {
				t.Error("NewMCPClient should return non-nil client")
			}

			if client.connections == nil {
				t.Error("connections map should be initialized")
			}

			if client.ctx == nil {
				t.Error("context should be initialized")
			}
		})
	}
}

func TestDefaultMCPRuntimeClientConfig(t *testing.T) {
	config := DefaultMCPRuntimeClientConfig()

	if config == nil {
		t.Error("DefaultMCPRuntimeClientConfig should return non-nil config")
	}

	if config.DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %v, want 30s", config.DefaultTimeout)
	}

	if config.DefaultRetries != 3 {
		t.Errorf("DefaultRetries = %v, want 3", config.DefaultRetries)
	}

	if config.DefaultRetryDelay != 1*time.Second {
		t.Errorf("DefaultRetryDelay = %v, want 1s", config.DefaultRetryDelay)
	}

	if !config.EnableFallback {
		t.Error("EnableFallback should be true by default")
	}
}

func TestMCPClient_Connect(t *testing.T) {
	tests := []struct {
		name       string
		config     *MCPRuntimeClientConfig
		serverName string
		wantErr    bool
	}{
		{
			name: "server not found",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{},
			},
			serverName: "nonexistent",
			wantErr:    true,
		},
		{
			name: "server disabled",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{
					{Name: "disabled-server", Enabled: false},
				},
			},
			serverName: "disabled-server",
			wantErr:    true,
		},
		{
			name: "missing command and endpoint",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{
					{Name: "invalid-server", Enabled: true},
				},
			},
			serverName: "invalid-server",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewMCPClient(tt.config)

			err := client.Connect(tt.serverName)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestMCPClient_ConnectAll(t *testing.T) {
	tests := []struct {
		name    string
		config  *MCPRuntimeClientConfig
		wantErr bool
	}{
		{
			name: "no servers",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{},
			},
			wantErr: false,
		},
		{
			name: "all servers disabled",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{
					{Name: "server1", Enabled: false},
					{Name: "server2", Enabled: false},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid server with fallback enabled",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{
					{Name: "invalid", Enabled: true},
				},
				EnableFallback: true,
			},
			wantErr: false,
		},
		{
			name: "invalid server with fallback disabled",
			config: &MCPRuntimeClientConfig{
				Servers: []MCPRuntimeServerConfig{
					{Name: "invalid", Enabled: true},
				},
				EnableFallback: false,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewMCPClient(tt.config)

			err := client.ConnectAll()

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestMCPClient_CallTool(t *testing.T) {
	client := NewMCPClient(&MCPRuntimeClientConfig{
		Servers: []MCPRuntimeServerConfig{},
	})

	result := client.CallTool("nonexistent-server", "test-tool", map[string]interface{}{})

	if result.Error == nil {
		t.Error("expected error for nonexistent server")
	}

	if result.ServerName != "nonexistent-server" {
		t.Errorf("ServerName = %v, want nonexistent-server", result.ServerName)
	}
}

func TestMCPClient_ListTools(t *testing.T) {
	client := NewMCPClient(&MCPRuntimeClientConfig{
		Servers: []MCPRuntimeServerConfig{},
	})

	_, err := client.ListTools("nonexistent-server")

	if err == nil {
		t.Error("expected error for nonexistent server")
	}
}

func TestMCPClient_ListAllTools(t *testing.T) {
	client := NewMCPClient(&MCPRuntimeClientConfig{
		Servers: []MCPRuntimeServerConfig{},
	})

	tools, err := client.ListAllTools()

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if tools == nil {
		t.Error("tools map should not be nil")
	}
}

func TestMCPClient_CallToolOnAny(t *testing.T) {
	client := NewMCPClient(&MCPRuntimeClientConfig{
		Servers: []MCPRuntimeServerConfig{},
	})

	result := client.CallToolOnAny("test-tool", map[string]interface{}{})

	if result.Error == nil {
		t.Error("expected error when no servers available")
	}
}

func TestMCPClient_IsConnected(t *testing.T) {
	client := NewMCPClient(nil)

	if client.IsConnected("nonexistent") {
		t.Error("IsConnected should return false for nonexistent server")
	}
}

func TestMCPClient_IsAnyConnected(t *testing.T) {
	client := NewMCPClient(nil)

	if client.IsAnyConnected() {
		t.Error("IsAnyConnected should return false with no connections")
	}
}

func TestMCPClient_Close(t *testing.T) {
	client := NewMCPClient(nil)

	err := client.Close()

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMCPClient_CloseServer(t *testing.T) {
	client := NewMCPClient(nil)

	err := client.CloseServer("nonexistent")

	if err != nil {
		t.Errorf("unexpected error for nonexistent server: %v", err)
	}
}

func TestMCPClient_GetConnectionStatus(t *testing.T) {
	client := NewMCPClient(nil)

	status := client.GetConnectionStatus()

	if status == nil {
		t.Error("GetConnectionStatus should return non-nil map")
	}
}

func TestMCPClient_AddServer(t *testing.T) {
	client := NewMCPClient(nil)

	tests := []struct {
		name    string
		config  MCPRuntimeServerConfig
		wantErr bool
	}{
		{
			name: "add new server",
			config: MCPRuntimeServerConfig{
				Name: "test-server",
			},
			wantErr: false,
		},
		{
			name: "add duplicate server",
			config: MCPRuntimeServerConfig{
				Name: "test-server",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.AddServer(tt.config)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestMCPClient_RemoveServer(t *testing.T) {
	client := NewMCPClient(nil)

	err := client.RemoveServer("nonexistent")

	if err == nil {
		t.Error("expected error for nonexistent server")
	}
}

func TestMCPClient_GetConfig(t *testing.T) {
	config := &MCPRuntimeClientConfig{
		DefaultTimeout: 45 * time.Second,
	}
	client := NewMCPClient(config)

	returnedConfig := client.GetConfig()

	if returnedConfig == nil {
		t.Error("GetConfig should return non-nil config")
	}

	if returnedConfig.DefaultTimeout != 45*time.Second {
		t.Errorf("DefaultTimeout = %v, want 45s", returnedConfig.DefaultTimeout)
	}
}

func TestMCPClient_SetTimeout(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		timeout    time.Duration
		wantErr    bool
	}{
		{
			name:       "nonexistent server",
			serverName: "nonexistent",
			timeout:    60 * time.Second,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewMCPClient(nil)

			err := client.SetTimeout(tt.serverName, tt.timeout)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"timeout error", context.DeadlineExceeded, true},
		{"other error", context.Canceled, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTransientError(tt.err)
			if result != tt.expected {
				t.Errorf("isTransientError() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestContainsString(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"hello world", "world", true},
		{"hello world", "foo", false},
		{"short", "longer substring", false},
		{"", "", true},
		{"test", "", true},
	}

	for _, tt := range tests {
		result := containsString(tt.s, tt.substr)
		if result != tt.want {
			t.Errorf("containsString(%q, %q) = %v, want %v", tt.s, tt.substr, result, tt.want)
		}
	}
}

func TestNewMCPClientFromConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *MCPConfig
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name: "nil client config",
			config: &MCPConfig{
				Client: nil,
			},
		},
		{
			name: "with servers",
			config: &MCPConfig{
				Client: &MCPClientConfig{
					Enabled: true,
					Servers: []MCPServerDefinition{
						{Name: "test", Command: "test-cmd"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewMCPClientFromConfig(tt.config)

			if client == nil {
				t.Error("NewMCPClientFromConfig should return non-nil client")
			}
		})
	}
}

func TestToolCallResult(t *testing.T) {
	result := &ToolCallResult{
		Content:    "test content",
		ServerName: "test-server",
		Duration:   100 * time.Millisecond,
		Retries:    0,
	}

	if result.Content != "test content" {
		t.Errorf("Content = %v, want test content", result.Content)
	}

	if result.ServerName != "test-server" {
		t.Errorf("ServerName = %v, want test-server", result.ServerName)
	}

	if result.Duration != 100*time.Millisecond {
		t.Errorf("Duration = %v, want 100ms", result.Duration)
	}
}

func TestToolInfo(t *testing.T) {
	tool := ToolInfo{
		Name:        "test-tool",
		Description: "Test tool description",
		InputSchema: map[string]interface{}{"type": "object"},
		ServerName:  "test-server",
	}

	if tool.Name != "test-tool" {
		t.Errorf("Name = %v, want test-tool", tool.Name)
	}

	if tool.Description != "Test tool description" {
		t.Errorf("Description = %v, want Test tool description", tool.Description)
	}
}

func TestMCPConnection(t *testing.T) {
	conn := &MCPConnection{
		ServerConfig: MCPRuntimeServerConfig{
			Name:    "test-server",
			Timeout: 30 * time.Second,
		},
		Connected:  true,
		LastUsed:   time.Now(),
		ErrorCount: 0,
	}

	if !conn.Connected {
		t.Error("Connected should be true")
	}

	if conn.ServerConfig.Name != "test-server" {
		t.Errorf("ServerConfig.Name = %v, want test-server", conn.ServerConfig.Name)
	}
}

func TestMCPRuntimeServerConfig(t *testing.T) {
	config := MCPRuntimeServerConfig{
		Name:        "test-server",
		Endpoint:    "http://localhost:8080",
		Timeout:     30 * time.Second,
		MaxRetries:  3,
		RetryDelay:  1 * time.Second,
		Enabled:     true,
		Description: "Test server",
		Headers: map[string]string{
			"Authorization": "Bearer token",
		},
	}

	if config.Name != "test-server" {
		t.Errorf("Name = %v, want test-server", config.Name)
	}

	if config.Endpoint != "http://localhost:8080" {
		t.Errorf("Endpoint = %v, want http://localhost:8080", config.Endpoint)
	}

	if !config.Enabled {
		t.Error("Enabled should be true")
	}
}
