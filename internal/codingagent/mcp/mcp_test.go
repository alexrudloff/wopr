package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The test binary doubles as a stdio MCP server when WOPR_MCP_FAKE_SERVER is
// set, so stdio tests exercise a real child process.
func TestMain(m *testing.M) {
	if os.Getenv("WOPR_MCP_FAKE_SERVER") == "1" {
		if err := fakeServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type echoIn struct {
	Text string `json:"text"`
}

type sleepIn struct {
	Ms int `json:"ms"`
}

type noIn struct{}

type bigIn struct {
	Bytes int `json:"bytes"`
}

func fakeServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)
	sdk.AddTool(s, &sdk.Tool{Name: "echo", Description: "Echo text back.\nSecond line."}, func(_ context.Context, _ *sdk.CallToolRequest, in echoIn) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo: " + in.Text}}}, nil, nil
	})
	sdk.AddTool(s, &sdk.Tool{Name: "env", Description: "Report an environment variable."}, func(_ context.Context, _ *sdk.CallToolRequest, in echoIn) (*sdk.CallToolResult, any, error) {
		v, ok := os.LookupEnv(in.Text)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("%s=%q set=%t", in.Text, v, ok)}}}, nil, nil
	})
	sdk.AddTool(s, &sdk.Tool{Name: "sleep", Description: "Sleep."}, func(ctx context.Context, _ *sdk.CallToolRequest, in sleepIn) (*sdk.CallToolResult, any, error) {
		select {
		case <-time.After(time.Duration(in.Ms) * time.Millisecond):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "slept"}}}, nil, nil
	})
	sdk.AddTool(s, &sdk.Tool{Name: "crash", Description: "Exit the server."}, func(context.Context, *sdk.CallToolRequest, noIn) (*sdk.CallToolResult, any, error) {
		fmt.Fprintln(os.Stderr, "fake server: crashing on request")
		os.Exit(3)
		return nil, nil, nil
	})
	sdk.AddTool(s, &sdk.Tool{Name: "big", Description: "Return a large text."}, func(_ context.Context, _ *sdk.CallToolRequest, in bigIn) (*sdk.CallToolResult, any, error) {
		line := strings.Repeat("x", 99) + "\n"
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.Repeat(line, in.Bytes/100)}}}, nil, nil
	})
	sdk.AddTool(s, &sdk.Tool{Name: "picture", Description: "Return an image and a resource."}, func(context.Context, *sdk.CallToolRequest, noIn) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: "here"},
			&sdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"},
			&sdk.ResourceLink{Name: "doc", URI: "file:///doc.txt"},
		}}, nil, nil
	})
	sdk.AddTool(s, &sdk.Tool{Name: "fail", Description: "Report a tool error."}, func(context.Context, *sdk.CallToolRequest, noIn) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "boom"}}}, nil, nil
	})
	return s
}

// inMemoryManager serves every configured server from an in-process fake.
func inMemoryManager(t *testing.T, cfg Config) (*Manager, *atomic.Int32) {
	t.Helper()
	var dials atomic.Int32
	m := NewManager(cfg, t.TempDir(), "test")
	m.Transport = func(string, ServerConfig) (sdk.Transport, error) {
		dials.Add(1)
		client, srv := sdk.NewInMemoryTransports()
		go func() { _ = fakeServer().Run(context.Background(), srv) }()
		return client, nil
	}
	t.Cleanup(m.Close)
	return m, &dials
}

func oneServer(name string, sc ServerConfig) Config {
	if sc.Command == "" && sc.URL == "" {
		sc.Command = "unused"
	}
	return Config{Servers: map[string]ServerConfig{name: sc}}
}

func run(t *testing.T, tool *ProxyTool, params string) (string, error) {
	t.Helper()
	res, err := tool.Execute(context.Background(), "call-1", json.RawMessage(params), nil)
	return res.Content, err
}

func TestServerEnvKeepsSecretsOut(t *testing.T) {
	env := serverEnv([]string{"PATH=/bin", "HOME=/h", "ANTHROPIC_API_KEY=sk-secret", "LC_ALL=C", "TOKEN=override-me"}, map[string]string{"TOKEN": "mine"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "sk-secret") {
		t.Errorf("provider key leaked into the server env: %v", env)
	}
	for _, want := range []string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "TOKEN=mine"} {
		if !slices.Contains(env, want) {
			t.Errorf("env %v lacks %s", env, want)
		}
	}
}

func TestProxyStartsServersLazilyAndListsDescribesCalls(t *testing.T) {
	m, dials := inMemoryManager(t, oneServer("fake", ServerConfig{}))
	tool := NewProxyTool(m)
	if !strings.Contains(tool.Schema().Description, "Servers: fake.") {
		t.Errorf("description does not name the servers: %q", tool.Schema().Description)
	}
	if dials.Load() != 0 || m.Status()[0].State != StateIdle {
		t.Fatal("a server started before first use")
	}

	out, err := run(t, tool, `{"action":"list"}`)
	if err != nil || !strings.HasPrefix(out, "fake: ") || !strings.Contains(out, "echo") {
		t.Fatalf("list = %q, %v", out, err)
	}
	out, _ = run(t, tool, `{"action":"list","tool":"fake"}`)
	if !strings.Contains(out, "fake/echo: Echo text back.") || strings.Contains(out, "Second line") {
		t.Errorf("server list = %q, want one-line descriptions", out)
	}
	out, err = run(t, tool, `{"action":"describe","tool":"fake/echo"}`)
	if err != nil || !strings.Contains(out, `"text"`) {
		t.Errorf("describe = %q, %v", out, err)
	}
	out, err = run(t, tool, `{"action":"call","tool":"fake/echo","args":{"text":"hi"}}`)
	if err != nil || out != "echo: hi" {
		t.Errorf("call = %q, %v", out, err)
	}
	out, err = run(t, tool, `{"action":"call","tool":"mcp__fake__echo","args":{"text":"alias"}}`)
	if err != nil || out != "echo: alias" {
		t.Errorf("call by direct name = %q, %v", out, err)
	}
	if dials.Load() != 1 {
		t.Errorf("dials = %d, want one connection reused", dials.Load())
	}
	if st := m.Status()[0]; st.State != StateReady || st.Tools != 7 {
		t.Errorf("status = %+v", st)
	}
}
