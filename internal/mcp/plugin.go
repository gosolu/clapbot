package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/react"
)

// Plugin implements react.Plugin and react.Closer, connecting configured MCP servers
// and registering their tools into a gosolu/react Agent.
type Plugin struct {
	servers map[string]config.MCPServerConfig
	clients []*Client
}

// NewPlugin creates an MCP Plugin for gosolu/react.
func NewPlugin(servers map[string]config.MCPServerConfig) *Plugin {
	return &Plugin{servers: servers}
}

// Name implements react.Plugin.
func (p *Plugin) Name() string {
	return "mcp"
}

// Init implements react.Plugin.
func (p *Plugin) Init(ctx context.Context, r react.Registrar) error {
	for srvName, srvCfg := range p.servers {
		if strings.TrimSpace(srvCfg.Command) == "" && strings.TrimSpace(srvCfg.URL) == "" {
			continue
		}
		client, err := Connect(ctx, srvName, srvCfg)
		if err != nil {
			log.Printf("[mcp] warning: failed to connect server %q: %v", srvName, err)
			continue
		}
		p.clients = append(p.clients, client)

		tools, err := client.ListTools(ctx)
		if err != nil {
			log.Printf("[mcp] warning: failed to list tools from %q: %v", srvName, err)
			continue
		}
		for _, td := range tools {
			c := client
			origName := td.Name
			qualifiedName := fmt.Sprintf("mcp__%s__%s", sanitizeName(srvName), sanitizeName(origName))
			schema := td.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			spec := react.ToolSpec{
				Name:        qualifiedName,
				Description: fmt.Sprintf("[MCP:%s] %s", srvName, td.Description),
				InputSchema: schema,
			}
			tool := react.NewRawTool(spec, func(callCtx context.Context, args json.RawMessage) (react.ToolResult, error) {
				out, isErr, err := c.CallTool(callCtx, origName, args)
				if err != nil {
					return react.ErrorResult(err.Error()), nil
				}
				if isErr {
					return react.ErrorResult(out), nil
				}
				return react.TextResult(out), nil
			})
			if err := r.AddTool(tool); err != nil {
				log.Printf("[mcp] warning: failed to register tool %q: %v", qualifiedName, err)
			}
		}
	}
	return nil
}

// Close implements react.Closer.
func (p *Plugin) Close() error {
	for _, c := range p.clients {
		_ = c.Close()
	}
	p.clients = nil
	return nil
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
