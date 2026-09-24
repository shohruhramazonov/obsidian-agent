// Package bot runs a minimal Telegram front end for the existing contact
// agent: it turns incoming text messages and business-card photos into
// Obsidian notes using the same agent.Agent used by the CLI.
package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/agent"
	"obsidian-agent/client"
	"obsidian-agent/model"
)

const usageMessage = `Send me a text description of a contact, or a photo of a business card, and I'll save it to your Obsidian vault.

Example:
John Smith, Google, Software Engineer, +998901234567, john@gmail.com`

// contactAgent is the subset of agent.Agent the bot depends on.
type contactAgent interface {
	CreateContact(ctx context.Context, input string) (*model.Contact, error)
	CreateContactFromImage(ctx context.Context, imagePath string) (*model.Contact, error)
}

// fileSaver is the subset of client.PairingClient used to save notes to the
// Obsidian agent paired with a Telegram user.
type fileSaver interface {
	SaveFile(ctx context.Context, telegramUserID int64, path, content string) error
}

// telegramAPI is the subset of tgbotapi.BotAPI the bot depends on.
type telegramAPI interface {
	GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
	GetFileDirectURL(fileID string) (string, error)
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
}

// Bot receives Telegram updates via long polling and forwards them to a
// contactAgent.
type Bot struct {
	api     telegramAPI
	agent   contactAgent
	pairer  pairer         // optional; enables pairing with codes shown by local agents
	saver   fileSaver      // optional; saves contacts to the user's paired agent
	folders folderSelector // optional; enables /folder and folder selection after pairing

	pairingLimiter attemptLimiter // failed pairing attempts per user

	mu           sync.Mutex
	folderNames  map[int64]string // folder each user selected through this bot, for replies only
	pairedAgents map[int64]string // users seen paired → agent ID ("" if unknown); the server owns the mapping
}

// New creates a Bot authenticated with the given Telegram bot token.
func New(token string, a contactAgent) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &Bot{api: api, agent: a}, nil
}

// EnablePairing lets unpaired users pair with their local agent by sending
// the pairing code it shows in its terminal.
func (b *Bot) EnablePairing(p pairer) {
	b.pairer = p
}

// EnableSaving makes the bot save contacts to the Obsidian agent paired with
// the sender instead of sending the Markdown back to Telegram.
func (b *Bot) EnableSaving(s fileSaver) {
	b.saver = s
}

// EnableFolders lets users choose, after pairing and with /folder, which
// folder of their vault receives their contacts.
func (b *Bot) EnableFolders(f folderSelector) {
	b.folders = f
}

// Run registers the command menu, then polls for Telegram updates until ctx
// is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	b.registerCommandsOrLog()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)
	defer b.api.StopReceivingUpdates()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			b.handleUpdate(ctx, update)
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
		return
	}

	msg := update.Message
	if msg == nil || msg.Chat == nil {
		return
	}

	switch {
	case msg.IsCommand() && msg.Command() == "start" && b.pairer != nil:
		b.handleStart(ctx, msg)
	case msg.IsCommand() && msg.Command() == "start":
		b.reply(msg.Chat.ID, usageMessage)
	case msg.IsCommand() && msg.Command() == "folder" && b.folders != nil:
		b.handleFolderCommand(ctx, msg)
	case msg.IsCommand() && msg.Command() == "folder":
		b.reply(msg.Chat.ID, usageMessage)
	case msg.IsCommand() && msg.Command() == "help":
		b.reply(msg.Chat.ID, helpText)
	case len(msg.Photo) > 0:
		b.handlePhoto(ctx, msg)
	case strings.TrimSpace(msg.Text) != "":
		b.handleText(ctx, msg)
	default:
		b.reply(msg.Chat.ID, usageMessage)
	}
}

func (b *Bot) handleText(ctx context.Context, msg *tgbotapi.Message) {
	if b.pairer != nil && msg.From != nil && looksLikePairingCode(msg.Text) {
		b.handlePairingCode(ctx, msg, strings.TrimSpace(msg.Text))
		return
	}

	contact, err := b.agent.CreateContact(ctx, msg.Text)
	if err != nil {
		b.replyError(msg.Chat.ID, err)
		return
	}

	b.replyContact(ctx, msg, contact)
}

func (b *Bot) handlePhoto(ctx context.Context, msg *tgbotapi.Message) {
	fileID := largestPhoto(msg.Photo).FileID

	path, err := b.downloadPhoto(fileID)
	if err != nil {
		b.replyError(msg.Chat.ID, err)
		return
	}
	defer os.Remove(path)

	contact, err := b.agent.CreateContactFromImage(ctx, path)
	if err != nil {
		b.replyError(msg.Chat.ID, err)
		return
	}

	b.replyContact(ctx, msg, contact)
}

// largestPhoto returns the highest-resolution size Telegram sent for a
// photo message.
func largestPhoto(sizes []tgbotapi.PhotoSize) tgbotapi.PhotoSize {
	largest := sizes[0]
	for _, s := range sizes[1:] {
		if s.Width*s.Height > largest.Width*largest.Height {
			largest = s
		}
	}

	return largest
}

// downloadPhoto downloads a Telegram file to a temporary file and returns
// its path. The caller is responsible for removing it.
func (b *Bot) downloadPhoto(fileID string) (string, error) {
	link, err := b.api.GetFileDirectURL(fileID)
	if err != nil {
		return "", fmt.Errorf("get file: %w", err)
	}

	resp, err := http.Get(link)
	if err != nil {
		return "", fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download file: unexpected status %s", resp.Status)
	}

	ext := filepath.Ext(link)
	if ext == "" {
		ext = ".jpg"
	}

	tmp, err := os.CreateTemp("", "obsidian-agent-photo-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("save file: %w", err)
	}

	return tmp.Name(), nil
}

func (b *Bot) replyContact(ctx context.Context, msg *tgbotapi.Message, c *model.Contact) {
	if b.saver == nil {
		b.sendContactDocument(msg.Chat.ID, c)
		return
	}

	text, keyboard := b.saveContact(ctx, msg.From, c)

	m := tgbotapi.NewMessage(msg.Chat.ID, text)
	if keyboard != nil {
		m.ReplyMarkup = *keyboard
	}
	if _, err := b.api.Send(m); err != nil {
		log.Printf("telegram: failed to send message: %v", err)
	}
}

const (
	notConnectedText     = "❌ Obsidian is not connected.\nPlease use /start to connect your Obsidian."
	agentOfflineText     = "❌ Your Obsidian agent is not connected.\nStart your local agent and try again."
	noFolderSelectedText = "Please choose a folder first."
)

// saveContact saves the contact's note to the folder the sender selected
// and returns the reply for Telegram, with a keyboard if the user has to act
// first. Only the file name is sent; the MCP server puts it in the selected
// folder. Errors are logged, never sent to the user.
func (b *Bot) saveContact(ctx context.Context, from *tgbotapi.User, c *model.Contact) (string, *tgbotapi.InlineKeyboardMarkup) {
	if from == nil {
		return notConnectedText, nil
	}

	if err := b.saver.SaveFile(ctx, from.ID, contactDocumentName(c), agent.GenerateMarkdown(c)); err != nil {
		log.Printf("telegram: save contact for user %d: %v", from.ID, err)
		if errors.Is(err, client.ErrNotPaired) || errors.Is(err, client.ErrNoFolderSelected) {
			b.setFolderName(from.ID, "")
		}
		if errors.Is(err, client.ErrNotPaired) {
			b.forgetPaired(from.ID)
		}
		if errors.Is(err, client.ErrNoFolderSelected) && b.folders != nil {
			return noFolderSelectedText, chooseFolderKeyboard(from.ID)
		}
		return saveFailedText(err), nil
	}

	if name := b.folderName(from.ID); name != "" {
		return fmt.Sprintf("✅ Contact saved to %s: %s", name, c.Name), nil
	}

	return fmt.Sprintf("✅ Contact saved to Obsidian: %s", c.Name), nil
}

// saveFailedText renders a save error without exposing internal details.
func saveFailedText(err error) string {
	switch {
	case errors.Is(err, client.ErrNotPaired):
		return notConnectedText
	case errors.Is(err, client.ErrNoFolderSelected):
		return noFolderSelectedText
	case errors.Is(err, client.ErrAgentOffline):
		return agentOfflineText
	}

	return "❌ Failed to save contact to Obsidian\nReason: " + failureReason(err)
}

// failureReason describes an Obsidian agent error without exposing internal
// details.
func failureReason(err error) string {
	switch {
	case errors.Is(err, client.ErrAgentOffline):
		return "your Obsidian agent is offline"
	case errors.Is(err, client.ErrAgentTimeout), errors.Is(err, context.DeadlineExceeded):
		return "your Obsidian agent did not respond in time"
	default:
		return "unexpected error, please try again later"
	}
}

// sendContactDocument sends the contact's Markdown note back as a Telegram
// document. It is used when no Obsidian agent saving is configured.
func (b *Bot) sendContactDocument(chatID int64, c *model.Contact) {
	b.reply(chatID, contactReplyText(c))

	markdown := agent.GenerateMarkdown(c)
	tmp, err := os.CreateTemp("", "obsidian-agent-contact-*.md")
	if err != nil {
		log.Printf("telegram: create temp markdown file: %v", err)
		return
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(markdown); err != nil {
		_ = tmp.Close()
		log.Printf("telegram: write temp markdown file: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		log.Printf("telegram: close temp markdown file: %v", err)
		return
	}

	file, err := os.Open(tmp.Name())
	if err != nil {
		log.Printf("telegram: open temp markdown file: %v", err)
		return
	}
	defer file.Close()

	cfg := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{
		Name:   contactDocumentName(c),
		Reader: file,
	})
	if _, err := b.api.Send(cfg); err != nil {
		log.Printf("telegram: send markdown document: %v", err)
	}
}

// contactReplyText renders the confirmation sent back to the user after a
// contact was created.
func contactReplyText(c *model.Contact) string {
	return fmt.Sprintf("Contact saved to Obsidian:\n%s", agent.FilePath(c.Name))
}

func contactDocumentName(c *model.Contact) string {
	return filepath.Base(agent.FilePath(c.Name))
}

func (b *Bot) replyError(chatID int64, err error) {
	log.Printf("telegram: %v", err)
	b.reply(chatID, "Please send valid contact or business-card data.")
}

func (b *Bot) reply(chatID int64, text string) {
	if _, err := b.api.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("telegram: failed to send message: %v", err)
	}
}
