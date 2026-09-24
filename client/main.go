package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ObsidianClient struct {
	session *mcp.ClientSession
}

type authTransport struct {
	apiKey string
	base   http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	return t.base.RoundTrip(req)
}

func New(
	ctx context.Context,
	url string,
	apiKey string,
) (*ObsidianClient, error) {
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "obsidian-agent",
			Version: "0.1.0",
		},
		nil,
	)

	transport := &mcp.StreamableClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{
				apiKey: apiKey,
				base:   http.DefaultTransport,
			},
		},
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}

	return &ObsidianClient{
		session: session,
	}, nil
}

func (c *ObsidianClient) Close() {
	c.session.Close()
}

func (c *ObsidianClient) Write(
	ctx context.Context,
	path string,
	content string,
) error {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name: "vault_write",
		Arguments: map[string]any{
			"path":    path,
			"content": content,
		},
	})
	if err != nil {
		return err
	}

	if result.IsError {
		return fmt.Errorf("obsidian vault_write failed")
	}

	return nil
}
