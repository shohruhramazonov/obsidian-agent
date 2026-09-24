package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"obsidian-agent/model"
)

// PairingClient talks to the obsidian-mcp server's pairing tools.
type PairingClient struct {
	session *mcp.ClientSession
}

// NewPairingClient connects to the obsidian-mcp server's MCP-over-HTTP
// endpoint, e.g. http://127.0.0.1:8080/mcp.
func NewPairingClient(ctx context.Context, url string) (*PairingClient, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "obsidian-agent-bot", Version: "0.1.0"}, nil)

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to pairing server: %w", err)
	}

	return &PairingClient{session: session}, nil
}

func (c *PairingClient) Close() {
	c.session.Close()
}

// PairAgent links a Telegram user to the local Obsidian agent that shows
// pairingCode in its terminal, and returns that agent's ID. The code is sent
// as typed; the server normalizes case, dashes and spaces.
func (c *PairingClient) PairAgent(ctx context.Context, telegramUserID int64, pairingCode string) (string, error) {
	var out struct {
		AgentID string `json:"agent_id"`
	}
	args := map[string]any{"telegram_user_id": telegramUserID, "pairing_code": pairingCode}
	if err := c.call(ctx, "pair_agent", args, &out); err != nil {
		return "", err
	}

	return out.AgentID, nil
}

// IsPaired reports whether a Telegram user is paired with an Obsidian agent.
// The server has no dedicated tool for this, so it asks list_folders, which
// checks the pairing before contacting the agent: only ErrNotPaired means
// unpaired, while an offline or slow agent is still paired.
func (c *PairingClient) IsPaired(ctx context.Context, telegramUserID int64) (bool, error) {
	_, err := c.ListFolders(ctx, telegramUserID)
	switch {
	case err == nil, errors.Is(err, ErrAgentOffline), errors.Is(err, ErrAgentTimeout):
		return true, nil
	case errors.Is(err, ErrNotPaired):
		return false, nil
	default:
		return false, err
	}
}

// Errors returned by SaveFile, ListFolders, SelectFolder and PairAgent for
// failures the user can act on.
var (
	ErrNotPaired        = errors.New("telegram user is not paired with an Obsidian agent")
	ErrAgentOffline     = errors.New("obsidian agent is not connected")
	ErrAgentTimeout     = errors.New("obsidian agent did not respond in time")
	ErrNoFolderSelected = errors.New("telegram user has not selected a folder")
	ErrUnknownFolder    = errors.New("folder is not available on the paired Obsidian agent")

	ErrInvalidPairingCode = errors.New("invalid pairing code")
	ErrExpiredPairingCode = errors.New("pairing code has expired")
	ErrUsedPairingCode    = errors.New("pairing code has already been used")
)

// ListFolders lists the folders of the vault of the Obsidian agent paired
// with a Telegram user.
func (c *PairingClient) ListFolders(ctx context.Context, telegramUserID int64) ([]model.Folder, error) {
	var out struct {
		Folders []model.Folder `json:"folders"`
	}
	if err := c.call(ctx, "list_folders", map[string]any{"telegram_user_id": telegramUserID}, &out); err != nil {
		return nil, err
	}

	return out.Folders, nil
}

// SelectFolder makes save_file write a Telegram user's files to the given
// folder. The server checks that the folder exists.
func (c *PairingClient) SelectFolder(ctx context.Context, telegramUserID int64, folderID string) (model.Folder, error) {
	var out struct {
		Folder model.Folder `json:"folder"`
	}
	args := map[string]any{"telegram_user_id": telegramUserID, "folder_id": folderID}
	if err := c.call(ctx, "select_folder", args, &out); err != nil {
		return model.Folder{}, err
	}

	return out.Folder, nil
}

// SaveFile saves a file to the folder the Telegram user selected with
// SelectFolder; path is relative to that folder.
func (c *PairingClient) SaveFile(ctx context.Context, telegramUserID int64, path, content string) error {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name: "save_file",
		Arguments: map[string]any{
			"telegram_user_id": telegramUserID,
			"path":             path,
			"content":          content,
		},
	})
	if err != nil {
		return fmt.Errorf("save_file: %w", err)
	}
	if result.IsError {
		return toolError("save_file", toolErrorText(result))
	}

	return nil
}

// toolError maps a tool's error text to one of the exported errors, or
// wraps it as is.
func toolError(tool, text string) error {
	switch {
	case strings.Contains(text, "not paired"):
		return ErrNotPaired
	case strings.Contains(text, "No Obsidian agent connected"),
		strings.Contains(text, "disconnected"):
		return ErrAgentOffline
	case strings.Contains(text, "timed out"):
		return ErrAgentTimeout
	case strings.Contains(text, "no folder selected"):
		return ErrNoFolderSelected
	case strings.Contains(text, "unknown folder_id"):
		return ErrUnknownFolder
	case strings.Contains(text, "invalid pairing code"):
		return ErrInvalidPairingCode
	case strings.Contains(text, "pairing code has expired"):
		return ErrExpiredPairingCode
	case strings.Contains(text, "pairing code has already been used"):
		return ErrUsedPairingCode
	default:
		return fmt.Errorf("%s failed: %s", tool, text)
	}
}

func toolErrorText(result *mcp.CallToolResult) string {
	var parts []string
	for _, c := range result.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}

	return strings.Join(parts, " ")
}

// call invokes a tool and decodes its structured output into out.
func (c *PairingClient) call(ctx context.Context, tool string, args map[string]any, out any) error {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if result.IsError {
		return toolError(tool, toolErrorText(result))
	}

	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", tool, err)
	}

	return nil
}
