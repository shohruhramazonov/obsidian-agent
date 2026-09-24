package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/agent"
	"obsidian-agent/client"
	"obsidian-agent/model"
)

// fakeSaver records the last SaveFile call and returns err.
type fakeSaver struct {
	calls          int
	telegramUserID int64
	path, content  string
	err            error
}

func (f *fakeSaver) SaveFile(ctx context.Context, telegramUserID int64, path, content string) error {
	f.calls++
	f.telegramUserID, f.path, f.content = telegramUserID, path, content
	return f.err
}

var jerry = &model.Contact{Name: "Jerry M. Chen", Company: "Google"}

func TestSaveContactPairedUser(t *testing.T) {
	s := &fakeSaver{}
	b := &Bot{saver: s}

	b.saveContact(context.Background(), &tgbotapi.User{ID: 4242}, jerry)

	if s.calls != 1 {
		t.Fatalf("SaveFile called %d times, want 1", s.calls)
	}
	if s.telegramUserID != 4242 {
		t.Errorf("telegram_user_id = %d, want 4242", s.telegramUserID)
	}
	// Only the file name: the MCP server puts it in the selected folder.
	if s.path != "Jerry M. Chen.md" {
		t.Errorf("path = %q", s.path)
	}
	if s.content != agent.GenerateMarkdown(jerry) {
		t.Errorf("content is not the generated Markdown:\n%s", s.content)
	}
}

func TestSaveContactSuccessMessage(t *testing.T) {
	b := &Bot{saver: &fakeSaver{}}

	got, keyboard := b.saveContact(context.Background(), &tgbotapi.User{ID: 4242}, jerry)
	want := "✅ Contact saved to Obsidian: Jerry M. Chen"
	if got != want || keyboard != nil {
		t.Errorf("reply = %q, %v; want %q", got, keyboard, want)
	}
	if strings.Contains(got, "dataview") {
		t.Error("reply must not contain the Markdown")
	}

	b.setFolderName(4242, "Contacts")
	got, _ = b.saveContact(context.Background(), &tgbotapi.User{ID: 4242}, jerry)
	if want := "✅ Contact saved to Contacts: Jerry M. Chen"; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestSaveContactUnpairedUser(t *testing.T) {
	want := "❌ Obsidian is not connected.\nPlease use /start to connect your Obsidian."

	b := &Bot{saver: &fakeSaver{err: client.ErrNotPaired}}
	if got, _ := b.saveContact(context.Background(), &tgbotapi.User{ID: 4242}, jerry); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}

	// Messages without a sender can't be matched to a pairing.
	s := &fakeSaver{}
	b = &Bot{saver: s}
	if got, _ := b.saveContact(context.Background(), nil, jerry); got != want {
		t.Errorf("reply without sender = %q, want %q", got, want)
	}
	if s.calls != 0 {
		t.Errorf("SaveFile called without sender")
	}
}

func TestSaveContactFailureIsSafe(t *testing.T) {
	tests := []struct {
		err    error
		reason string
	}{
		{fmt.Errorf("wrapped: %w", client.ErrAgentTimeout), "your Obsidian agent did not respond in time"},
		{errors.New(`save_file failed: PUT http://127.0.0.1:27123/vault/x: Bearer secret-key: 500 goroutine 1 [running]`), "unexpected error, please try again later"},
	}

	for _, tt := range tests {
		b := &Bot{saver: &fakeSaver{err: tt.err}}
		got, _ := b.saveContact(context.Background(), &tgbotapi.User{ID: 4242}, jerry)

		want := "❌ Failed to save contact to Obsidian\nReason: " + tt.reason
		if got != want {
			t.Errorf("err %v: reply = %q, want %q", tt.err, got, want)
		}
		for _, leak := range []string{"secret", "http", "goroutine", "save_file", "500"} {
			if strings.Contains(got, leak) {
				t.Errorf("reply leaks %q: %q", leak, got)
			}
		}
	}
}

func TestSaveContactAgentOffline(t *testing.T) {
	b := &Bot{saver: &fakeSaver{err: client.ErrAgentOffline}}

	got, _ := b.saveContact(context.Background(), &tgbotapi.User{ID: 4242}, jerry)
	if got != agentOfflineText {
		t.Errorf("reply = %q, want %q", got, agentOfflineText)
	}
}
