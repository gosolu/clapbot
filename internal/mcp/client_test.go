package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gosolu/clapbot/internal/config"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "test-server",
						"version": "1.0",
					},
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		case "tools/list":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []ToolDef{
						{Name: "echo", Description: "echo tool"},
					},
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		}
	}
}

func TestClient_SubprocessLifetime(t *testing.T) {
	initCtx, cancelInit := context.WithCancel(context.Background())

	cfg := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	}

	client, err := Connect(initCtx, "test", cfg)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Cancel the initialization context. The subprocess must remain alive.
	cancelInit()

	time.Sleep(50 * time.Millisecond)

	callCtx, cancelCall := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCall()

	tools, err := client.ListTools(callCtx)
	if err != nil {
		t.Fatalf("ListTools failed after initCtx cancelled: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}
