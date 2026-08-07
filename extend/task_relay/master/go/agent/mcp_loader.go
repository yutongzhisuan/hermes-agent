package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	omcp "github.com/cloudwego/eino-ext/components/tool/mcp/officialmcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPToolkit holds tools loaded from one or more MCP servers and their sessions.
type MCPToolkit struct {
	Tools    []tool.BaseTool
	sessions []*mcp.ClientSession
}

// Close closes all MCP client sessions.
func (t *MCPToolkit) Close() error {
	if t == nil {
		return nil
	}
	var first error
	for i := len(t.sessions) - 1; i >= 0; i-- {
		if err := t.sessions[i].Close(); err != nil && first == nil {
			first = err
		}
	}
	t.sessions = nil
	return first
}

// LoadMCPTools connects to configured MCP servers and returns Eino tools.
// Connection failure for any enabled server fails the whole load.
func LoadMCPTools(ctx context.Context, servers map[string]MCPServerConfig) (*MCPToolkit, error) {
	if len(servers) == 0 {
		return &MCPToolkit{}, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "task-relay-master", Version: "v1.0.0"}, nil)
	out := &MCPToolkit{}
	for name, srv := range servers {
		if srv.Disabled {
			continue
		}
		session, tools, err := connectMCPServer(ctx, client, name, srv)
		if err != nil {
			_ = out.Close()
			return nil, err
		}
		out.sessions = append(out.sessions, session)
		out.Tools = append(out.Tools, tools...)
	}
	return out, nil
}

func connectMCPServer(
	ctx context.Context,
	client *mcp.Client,
	name string,
	srv MCPServerConfig,
) (*mcp.ClientSession, []tool.BaseTool, error) {
	transport, err := buildMCPTransport(name, srv)
	if err != nil {
		return nil, nil, err
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp server %q connect: %w", name, err)
	}
	tools, err := omcp.GetTools(ctx, &omcp.Config{
		Cli:          session,
		ToolNameList: srv.Tools,
	})
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("mcp server %q list tools: %w", name, err)
	}
	return session, tools, nil
}

func buildMCPTransport(name string, srv MCPServerConfig) (mcp.Transport, error) {
	kind, err := resolveMCPTransport(srv)
	if err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	switch kind {
	case "stdio":
		cmd := exec.Command(srv.Command, srv.Args...)
		cmd.Env = mergeProcessEnv(srv.Env)
		cmd.Stderr = os.Stderr
		return &mcp.CommandTransport{Command: cmd}, nil
	case "sse":
		return &mcp.SSEClientTransport{
			Endpoint:   srv.URL,
			HTTPClient: httpClientWithHeaders(srv.Headers),
		}, nil
	case "http":
		return &mcp.StreamableClientTransport{
			Endpoint:   srv.URL,
			HTTPClient: httpClientWithHeaders(srv.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("mcp server %q: unsupported transport %q", name, kind)
	}
}

func resolveMCPTransport(srv MCPServerConfig) (string, error) {
	t := strings.ToLower(strings.TrimSpace(srv.Type))
	switch t {
	case "stdio", "command":
		if strings.TrimSpace(srv.Command) == "" {
			return "", fmt.Errorf("stdio transport requires command")
		}
		return "stdio", nil
	case "sse":
		if strings.TrimSpace(srv.URL) == "" {
			return "", fmt.Errorf("sse transport requires url")
		}
		return "sse", nil
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		if strings.TrimSpace(srv.URL) == "" {
			return "", fmt.Errorf("http transport requires url")
		}
		return "http", nil
	case "":
		if strings.TrimSpace(srv.Command) != "" {
			return "stdio", nil
		}
		if strings.TrimSpace(srv.URL) != "" {
			return "http", nil
		}
		return "", fmt.Errorf("need command (stdio) or url (http/sse)")
	default:
		return "", fmt.Errorf("unknown type %q (use stdio|sse|http)", srv.Type)
	}
}

func mergeProcessEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{Transport: headerRoundTripper{headers: headers}}
}
