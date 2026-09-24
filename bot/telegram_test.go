package bot

import (
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/model"
)

func TestLargestPhoto(t *testing.T) {
	sizes := []tgbotapi.PhotoSize{
		{FileID: "small", Width: 90, Height: 90},
		{FileID: "large", Width: 1280, Height: 960},
		{FileID: "medium", Width: 320, Height: 240},
	}

	got := largestPhoto(sizes)
	if got.FileID != "large" {
		t.Errorf("largestPhoto() = %q, want %q", got.FileID, "large")
	}
}

func TestLargestPhoto_SingleSize(t *testing.T) {
	sizes := []tgbotapi.PhotoSize{{FileID: "only", Width: 100, Height: 100}}

	got := largestPhoto(sizes)
	if got.FileID != "only" {
		t.Errorf("largestPhoto() = %q, want %q", got.FileID, "only")
	}
}

func TestContactReplyText(t *testing.T) {
	c := &model.Contact{
		Name:    "Jerry M. Chen",
		Company: "Google",
	}

	got := contactReplyText(c)
	want := "Contact saved to Obsidian:\nContacts/Jerry M. Chen.md"

	if got != want {
		t.Errorf("contactReplyText() = %q, want %q", got, want)
	}
}

func TestContactDocumentName(t *testing.T) {
	c := &model.Contact{Name: "Jerry M. Chen"}

	got := contactDocumentName(c)
	want := "Jerry M. Chen.md"

	if got != want {
		t.Errorf("contactDocumentName() = %q, want %q", got, want)
	}
}
