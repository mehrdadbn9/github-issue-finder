package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPRuntimeServerConfig struct {
	Name        string            `json:"name"`
	Endpoint    string            `json:"endpoint"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	APIKey      string            `json:"apiKey,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Timeout     time.Duration     `json:"timeout"`
	MaxRetries  int               `json:"maxRetries"`
	RetryDelay  time.Duration     `json:"retryDelay"`
	Enabled     bool              `json:"enabled"`
	Description string            `json:"description"`
}

type MCPRuntimeClientConfig struct {
	Servers           []MCPRuntimeServerConfig `json:"servers"`
	DefaultTimeout    time.Duration            `json:"defaultTimeout"`
	DefaultRetries    int                      `json:"defaultRetries"`
	DefaultRetryDelay time.Duration            `json:"defaultRetryDelay"`
	EnableFallback    bool                     `json:"enableFallback"`
}

type MCPConnection struct {
	ServerConfig MCPRuntimeServerConfig
	Client       *mcp.Client
	Session      *mcp.ClientSession
	Connected    bool
	LastUsed     time.Time
	ErrorCount   int
	mu           sync.RWMutex
}

type MCPClient struct {
	config      *MCPRuntimeClientConfig
	connections map[string]*MCPConnection
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
}

type ToolCallResult struct {
	Content    string
	RawContent []mcp.Content
	Error      error
	ServerName string
	Duration   time.Duration
	Retries    int
}

type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
	ServerName  string
}

func DefaultMCPRuntimeClientConfig() *MCPRuntimeClientConfig {
	return &MCPRuntimeClientConfig{
		Servers:           []MCPRuntimeServerConfig{},
		DefaultTimeout:    30 * time.Second,
		DefaultRetries:    3,
		DefaultRetryDelay: 1 * time.Second,
		EnableFallback:    true,
	}
}

func NewMCPClient(config *MCPRuntimeClientConfig) *MCPClient {
	if config == nil {
		config = DefaultMCPRuntimeClientConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &MCPClient{
		config:      config,
		connections: make(map[string]*MCPConnection),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func NewMCPClientFromConfig(cfg *MCPConfig) *MCPClient {
	if cfg == nil || cfg.Client == nil {
		return NewMCPClient(nil)
	}

	config := DefaultMCPRuntimeClientConfig()
	config.Servers = make([]MCPRuntimeServerConfig, 0, len(cfg.Client.Servers))

	for _, s := range cfg.Client.Servers {
		serverConfig := MCPRuntimeServerConfig{
			Name:       s.Name,
			Command:    s.Command,
			Args:       s.Args,
			Endpoint:   s.Endpoint,
			APIKey:     s.APIKey,
			Enabled:    true,
			Timeout:    config.DefaultTimeout,
			MaxRetries: config.DefaultRetries,
			RetryDelay: config.DefaultRetryDelay,
		}
		config.Servers = append(config.Servers, serverConfig)
	}

	return NewMCPClient(config)
}

func (c *MCPClient) Connect(serverName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var serverConfig *MCPRuntimeServerConfig
	for i := range c.config.Servers {
		if c.config.Servers[i].Name == serverName {
			serverConfig = &c.config.Servers[i]
			break
		}
	}

	if serverConfig == nil {
		return fmt.Errorf("server %s not found in configuration", serverName)
	}

	if !serverConfig.Enabled {
		return fmt.Errorf("server %s is disabled", serverName)
	}

	if conn, exists := c.connections[serverName]; exists && conn.Connected {
		return nil
	}

	return c.connectToServer(serverConfig)
}

func (c *MCPClient) connectToServer(config *MCPRuntimeServerConfig) error {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = c.config.DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "github-issue-finder",
		Version: "1.0.0",
	}, &mcp.ClientOptions{})

	var transport mcp.Transport

	if config.Command != "" {
		cmd := exec.CommandContext(ctx, config.Command, config.Args...)
		transport = &mcp.CommandTransport{Command: cmd}
	} else if config.Endpoint != "" {
		transport = &mcp.StreamableClientTransport{
			Endpoint: config.Endpoint,
		}
	} else {
		return fmt.Errorf("server %s must have either Command or Endpoint configured", config.Name)
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server %s: %w", config.Name, err)
	}

	conn := &MCPConnection{
		ServerConfig: *config,
		Client:       client,
		Session:      session,
		Connected:    true,
		LastUsed:     time.Now(),
		ErrorCount:   0,
	}

	c.connections[config.Name] = conn

	return nil
}

func (c *MCPClient) ConnectAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errors []error

	for i := range c.config.Servers {
		if !c.config.Servers[i].Enabled {
			continue
		}

		if err := c.connectToServer(&c.config.Servers[i]); err != nil {
			errors = append(errors, fmt.Errorf("failed to connect to %s: %w", c.config.Servers[i].Name, err))
		}
	}

	if len(errors) > 0 && !c.config.EnableFallback {
		return fmt.Errorf("failed to connect to some servers: %v", errors)
	}

	return nil
}

func (c *MCPClient) CallTool(serverName, toolName string, params map[string]interface{}) *ToolCallResult {
	start := time.Now()
	result := &ToolCallResult{
		ServerName: serverName,
	}

	c.mu.RLock()
	conn, exists := c.connections[serverName]
	c.mu.RUnlock()

	if !exists || !conn.Connected {
		if err := c.Connect(serverName); err != nil {
			result.Error = fmt.Errorf("failed to connect to server %s: %w", serverName, err)
			return result
		}
		c.mu.RLock()
		conn = c.connections[serverName]
		c.mu.RUnlock()
	}

	maxRetries := conn.ServerConfig.MaxRetries
	if maxRetries == 0 {
		maxRetries = c.config.DefaultRetries
	}

	retryDelay := conn.ServerConfig.RetryDelay
	if retryDelay == 0 {
		retryDelay = c.config.DefaultRetryDelay
	}

	timeout := conn.ServerConfig.Timeout
	if timeout == 0 {
		timeout = c.config.DefaultTimeout
	}

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			result.Retries++
			time.Sleep(retryDelay * time.Duration(attempt))
		}

		ctx, cancel := context.WithTimeout(c.ctx, timeout)

		callParams := &mcp.CallToolParams{
			Name:      toolName,
			Arguments: params,
		}

		res, err := conn.Session.CallTool(ctx, callParams)
		cancel()

		if err != nil {
			lastErr = err
			conn.mu.Lock()
			conn.ErrorCount++
			conn.mu.Unlock()

			if isTransientError(err) {
				continue
			}
			break
		}

		conn.mu.Lock()
		conn.LastUsed = time.Now()
		conn.ErrorCount = 0
		conn.mu.Unlock()

		result.RawContent = res.Content
		result.Duration = time.Since(start)

		if len(res.Content) > 0 {
			if textContent, ok := res.Content[0].(*mcp.TextContent); ok {
				result.Content = textContent.Text
			} else {
				contentBytes, _ := json.Marshal(res.Content)
				result.Content = string(contentBytes)
			}
		}

		return result
	}

	result.Error = fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
	result.Duration = time.Since(start)
	return result
}

func (c *MCPClient) ListTools(serverName string) ([]ToolInfo, error) {
	c.mu.RLock()
	conn, exists := c.connections[serverName]
	c.mu.RUnlock()

	if !exists || !conn.Connected {
		if err := c.Connect(serverName); err != nil {
			return nil, fmt.Errorf("failed to connect to server %s: %w", serverName, err)
		}
		c.mu.RLock()
		conn = c.connections[serverName]
		c.mu.RUnlock()
	}

	timeout := conn.ServerConfig.Timeout
	if timeout == 0 {
		timeout = c.config.DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	result, err := conn.Session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	var tools []ToolInfo
	for _, tool := range result.Tools {
		inputSchema := make(map[string]interface{})
		if tool.InputSchema != nil {
			if schemaMap, ok := tool.InputSchema.(map[string]interface{}); ok {
				inputSchema = schemaMap
			}
		}
		tools = append(tools, ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: inputSchema,
			ServerName:  serverName,
		})
	}

	return tools, nil
}

func (c *MCPClient) ListAllTools() (map[string][]ToolInfo, error) {
	allTools := make(map[string][]ToolInfo)

	c.mu.RLock()
	defer c.mu.RUnlock()

	for name := range c.connections {
		tools, err := c.ListTools(name)
		if err != nil {
			continue
		}
		allTools[name] = tools
	}

	return allTools, nil
}

func (c *MCPClient) CallToolOnAny(toolName string, params map[string]interface{}) *ToolCallResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for name := range c.connections {
		result := c.CallTool(name, toolName, params)
		if result.Error == nil {
			return result
		}
	}

	return &ToolCallResult{
		Error: fmt.Errorf("no server could successfully call tool %s", toolName),
	}
}

func (c *MCPClient) IsConnected(serverName string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	conn, exists := c.connections[serverName]
	if !exists {
		return false
	}

	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.Connected
}

func (c *MCPClient) IsAnyConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, conn := range c.connections {
		conn.mu.RLock()
		connected := conn.Connected
		conn.mu.RUnlock()
		if connected {
			return true
		}
	}
	return false
}

func (c *MCPClient) Close() error {
	c.cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	var errors []error

	for name, conn := range c.connections {
		if conn.Session != nil {
			if err := conn.Session.Close(); err != nil {
				errors = append(errors, fmt.Errorf("failed to close session %s: %w", name, err))
			}
		}
		conn.Connected = false
	}

	c.connections = make(map[string]*MCPConnection)

	if len(errors) > 0 {
		return fmt.Errorf("errors during close: %v", errors)
	}

	return nil
}

func (c *MCPClient) CloseServer(serverName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, exists := c.connections[serverName]
	if !exists {
		return nil
	}

	if conn.Session != nil {
		if err := conn.Session.Close(); err != nil {
			return err
		}
	}

	delete(c.connections, serverName)
	return nil
}

func (c *MCPClient) GetConnectionStatus() map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := make(map[string]bool)
	for name, conn := range c.connections {
		conn.mu.RLock()
		status[name] = conn.Connected
		conn.mu.RUnlock()
	}
	return status
}

func (c *MCPClient) AddServer(config MCPRuntimeServerConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, existing := range c.config.Servers {
		if existing.Name == config.Name {
			return fmt.Errorf("server %s already exists", config.Name)
		}
	}

	if config.Timeout == 0 {
		config.Timeout = c.config.DefaultTimeout
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = c.config.DefaultRetries
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = c.config.DefaultRetryDelay
	}

	c.config.Servers = append(c.config.Servers, config)
	return nil
}

func (c *MCPClient) RemoveServer(serverName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if conn, exists := c.connections[serverName]; exists {
		if conn.Session != nil {
			_ = conn.Session.Close()
		}
		delete(c.connections, serverName)
	}

	for i, server := range c.config.Servers {
		if server.Name == serverName {
			c.config.Servers = append(c.config.Servers[:i], c.config.Servers[i+1:]...)
			return nil
		}
	}

	return fmt.Errorf("server %s not found", serverName)
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	transientIndicators := []string{
		"timeout",
		"connection reset",
		"connection refused",
		"temporary",
		"try again",
		"deadline exceeded",
		"context deadline",
	}

	for _, indicator := range transientIndicators {
		if containsString(errStr, indicator) {
			return true
		}
	}
	return false
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func (c *MCPClient) GetConfig() *MCPRuntimeClientConfig {
	return c.config
}

func (c *MCPClient) SetTimeout(serverName string, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.config.Servers {
		if c.config.Servers[i].Name == serverName {
			c.config.Servers[i].Timeout = timeout
			if conn, exists := c.connections[serverName]; exists {
				conn.ServerConfig.Timeout = timeout
			}
			return nil
		}
	}

	return fmt.Errorf("server %s not found", serverName)
}
