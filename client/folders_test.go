package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"obsidian-agent/model"
)

// fakeServer mimics the obsidian-mcp tools the bot uses and records the
// arguments it receives.
type fakeServer struct {
	folders   []model.Folder
	listErr   error
	selectErr error

	selectedUser   int64
	selectedFolder string
	saveArgs       map[string]any
	pairArgs       pairInput
	pairErr        error
}

type pairInput struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	PairingCode    string `json:"pairing_code"`
}

type pairOutput struct {
	AgentID        string `json:"agent_id"`
	AgentConnected bool   `json:"agent_connected"`
}

type userInput struct {
	TelegramUserID int64 `json:"telegram_user_id"`
}

type selectInput struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	FolderID       string `json:"folder_id"`
}

type listOutput struct {
	Folders []model.Folder `json:"folders"`
}

type selectOutput struct {
	Folder model.Folder `json:"folder"`
}

func newTestClient(t *testing.T, f *fakeServer) *PairingClient {
	t.Helper()
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "fake-obsidian-mcp", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list_folders"}, func(ctx context.Context, req *mcp.CallToolRequest, in userInput) (*mcp.CallToolResult, listOutput, error) {
		return nil, listOutput{Folders: f.folders}, f.listErr
	})
	mcp.AddTool(server, &mcp.Tool{Name: "select_folder"}, func(ctx context.Context, req *mcp.CallToolRequest, in selectInput) (*mcp.CallToolResult, selectOutput, error) {
		if f.selectErr != nil {
			return nil, selectOutput{}, f.selectErr
		}
		f.selectedUser, f.selectedFolder = in.TelegramUserID, in.FolderID
		for _, folder := range f.folders {
			if folder.ID == in.FolderID {
				return nil, selectOutput{Folder: folder}, nil
			}
		}
		return nil, selectOutput{}, errors.New("unknown folder_id: use list_folders to see the available folders")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "pair_agent"}, func(ctx context.Context, req *mcp.CallToolRequest, in pairInput) (*mcp.CallToolResult, pairOutput, error) {
		f.pairArgs = in
		if f.pairErr != nil {
			return nil, pairOutput{}, f.pairErr
		}
		return nil, pairOutput{AgentID: "agent-1", AgentConnected: true}, nil
	})
	server.AddTool(&mcp.Tool{Name: "save_file", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := json.Unmarshal(req.Params.Arguments, &f.saveArgs); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "File saved successfully"}}}, nil
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &PairingClient{session: session}
	t.Cleanup(c.Close)

	return c
}

func TestListFolders(t *testing.T) {
	want := []model.Folder{{ID: "contacts", Name: "Contacts", Path: "Contacts"}, {ID: "people", Name: "People", Path: "People"}}
	c := newTestClient(t, &fakeServer{folders: want})

	got, err := c.ListFolders(context.Background(), 4242)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ListFolders = %+v, want %+v", got, want)
	}
}

func TestListFoldersErrors(t *testing.T) {
	tests := []struct {
		serverErr error
		want      error
	}{
		{errors.New("This Telegram user is not paired with an Obsidian agent"), ErrNotPaired},
		{errors.New("No Obsidian agent connected"), ErrAgentOffline},
		{errors.New("timed out waiting for Obsidian agent response"), ErrAgentTimeout},
	}
	for _, tt := range tests {
		c := newTestClient(t, &fakeServer{listErr: tt.serverErr})
		if _, err := c.ListFolders(context.Background(), 4242); !errors.Is(err, tt.want) {
			t.Errorf("server error %q: got %v, want %v", tt.serverErr, err, tt.want)
		}
	}
}

func TestSelectFolder(t *testing.T) {
	contacts := model.Folder{ID: "contacts", Name: "Contacts", Path: "Contacts"}
	f := &fakeServer{folders: []model.Folder{contacts}}
	c := newTestClient(t, f)

	got, err := c.SelectFolder(context.Background(), 4242, "contacts")
	if err != nil {
		t.Fatal(err)
	}
	if got != contacts {
		t.Errorf("SelectFolder = %+v", got)
	}
	if f.selectedUser != 4242 || f.selectedFolder != "contacts" {
		t.Errorf("server got user %d, folder %q", f.selectedUser, f.selectedFolder)
	}

	if _, err := c.SelectFolder(context.Background(), 4242, "clients"); !errors.Is(err, ErrUnknownFolder) {
		t.Errorf("unknown folder: got %v, want ErrUnknownFolder", err)
	}
}

func TestSaveFileSendsTelegramUserID(t *testing.T) {
	f := &fakeServer{}
	c := newTestClient(t, f)

	if err := c.SaveFile(context.Background(), 4242, "Jerry M. Chen.md", "# Jerry"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.saveArgs["telegram_user_id"].(float64); got != 4242 {
		t.Errorf("telegram_user_id = %v, want 4242", f.saveArgs["telegram_user_id"])
	}
	for _, arg := range []string{"folder_id", "vault_id"} {
		if _, ok := f.saveArgs[arg]; ok {
			t.Errorf("save_file must not send %s; the server owns the selection", arg)
		}
	}
}
