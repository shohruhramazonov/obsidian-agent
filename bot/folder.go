package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/client"
	"obsidian-agent/model"
)

// folderCallTimeout bounds each list_folders / select_folder call.
const folderCallTimeout = 30 * time.Second

// Callback data of the folder buttons:
//
//	folder:<telegram user id>:<folder id>     select a folder
//	folder#:<telegram user id>:<index>        select by position in list_folders
//	folders:<telegram user id>                show the folder keyboard
//
// The index form is used only when the folder id does not fit Telegram's
// 64-byte limit. The user id binds a button to the user it was shown to;
// the folder itself is checked again by the MCP server on select_folder.
const (
	folderCallbackPrefix      = "folder:"
	folderIndexCallbackPrefix = "folder#:"
	folderMenuCallbackPrefix  = "folders:"
	maxCallbackDataLen        = 64
)

// folderSelector is the subset of client.PairingClient used to choose the
// folder that save_file writes to.
type folderSelector interface {
	ListFolders(ctx context.Context, telegramUserID int64) ([]model.Folder, error)
	SelectFolder(ctx context.Context, telegramUserID int64, folderID string) (model.Folder, error)
}

const (
	chooseFolderText = "Choose where to save contacts:"
	noFoldersText    = "⚠️ No folders are configured for this agent."
)

// handleFolderCommand shows the folder keyboard so the user can change the
// folder their contacts are saved to.
func (b *Bot) handleFolderCommand(ctx context.Context, msg *tgbotapi.Message) {
	if msg.From == nil {
		b.reply(msg.Chat.ID, notConnectedText)
		return
	}

	b.sendFolderMenu(ctx, msg.Chat.ID, msg.From.ID, b.currentFolderIntro(msg.From.ID))
}

func (b *Bot) currentFolderIntro(userID int64) string {
	if name := b.folderName(userID); name != "" {
		return "📁 Current folder: " + name
	}

	return ""
}

// sendFolderMenu sends intro with a keyboard of the user's folders, or intro
// and an explanation if they can't be listed.
func (b *Bot) sendFolderMenu(ctx context.Context, chatID, userID int64, intro string) {
	text, keyboard := b.folderMenu(ctx, userID, intro)

	m := tgbotapi.NewMessage(chatID, text)
	if keyboard != nil {
		m.ReplyMarkup = *keyboard
	}
	if _, err := b.api.Send(m); err != nil {
		log.Printf("telegram: failed to send folder menu: %v", err)
	}
}

// folderMenu returns the folder selection message for a user and its
// keyboard, which is nil if there is nothing to choose from.
func (b *Bot) folderMenu(ctx context.Context, userID int64, intro string) (string, *tgbotapi.InlineKeyboardMarkup) {
	ctx, cancel := context.WithTimeout(ctx, folderCallTimeout)
	defer cancel()

	folders, err := b.folders.ListFolders(ctx, userID)

	var text string
	var keyboard *tgbotapi.InlineKeyboardMarkup
	switch {
	case err != nil:
		log.Printf("telegram: list folders for user %d: %v", userID, err)
		if errors.Is(err, client.ErrNotPaired) {
			b.forgetPaired(userID)
		}
		text = listFoldersFailedText(err)
	case len(folders) == 0:
		text = noFoldersText
	default:
		text, keyboard = chooseFolderText, folderKeyboard(userID, folders)
	}

	if intro != "" {
		text = intro + "\n\n" + text
	}

	return text, keyboard
}

// folderKeyboard returns one button per folder, bound to userID.
func folderKeyboard(userID int64, folders []model.Folder) *tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, len(folders))
	for i, f := range folders {
		rows[i] = tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(folderLabel(f), folderCallbackData(userID, i, f)),
		)
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &keyboard
}

// chooseFolderKeyboard returns a single button that opens the folder
// keyboard for userID.
func chooseFolderKeyboard(userID int64) *tgbotapi.InlineKeyboardMarkup {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("📁 Choose folder", fmt.Sprintf("%s%d", folderMenuCallbackPrefix, userID)),
	))
	return &keyboard
}

// folderLabel is what the user sees for a folder; it never shows the
// folder's internal id.
func folderLabel(f model.Folder) string {
	switch {
	case f.Name != "":
		return f.Name
	case f.Path != "":
		return f.Path
	default:
		return "Unnamed folder"
	}
}

func folderCallbackData(userID int64, index int, f model.Folder) string {
	data := fmt.Sprintf("%s%d:%s", folderCallbackPrefix, userID, f.ID)
	if len(data) <= maxCallbackDataLen {
		return data
	}

	return fmt.Sprintf("%s%d:%d", folderIndexCallbackPrefix, userID, index)
}

// folderAction is a parsed folder button press.
type folderAction struct {
	userID   int64
	menu     bool   // folders:<user>
	folderID string // folder:<user>:<id>
	index    int    // folder#:<user>:<index>, used when folderID is empty
}

// parseFolderCallback parses the callback data of a folder button.
func parseFolderCallback(data string) (folderAction, bool) {
	if rest, ok := strings.CutPrefix(data, folderMenuCallbackPrefix); ok {
		userID, ok := parseUserID(rest)
		return folderAction{userID: userID, menu: true}, ok
	}

	prefix := folderCallbackPrefix
	if strings.HasPrefix(data, folderIndexCallbackPrefix) {
		prefix = folderIndexCallbackPrefix
	} else if !strings.HasPrefix(data, folderCallbackPrefix) {
		return folderAction{}, false
	}

	user, ref, ok := strings.Cut(strings.TrimPrefix(data, prefix), ":")
	if !ok || ref == "" {
		return folderAction{}, false
	}
	userID, ok := parseUserID(user)
	if !ok {
		return folderAction{}, false
	}

	if prefix == folderCallbackPrefix {
		return folderAction{userID: userID, folderID: ref}, true
	}
	index, err := strconv.Atoi(ref)
	if err != nil || index < 0 {
		return folderAction{}, false
	}

	return folderAction{userID: userID, index: index}, true
}

func parseUserID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id != 0
}

// handleCallback handles a folder button press: it checks that the button
// was shown to the user pressing it, then either shows the folder keyboard
// or selects the folder through the MCP server and replaces the keyboard
// with a confirmation.
func (b *Bot) handleCallback(ctx context.Context, q *tgbotapi.CallbackQuery) {
	action, ok := parseFolderCallback(q.Data)
	if !ok || b.folders == nil {
		b.answerCallback(q.ID, "")
		return
	}
	if q.From == nil || q.From.ID != action.userID {
		log.Printf("telegram: rejected folder button of user %d pressed by another user", action.userID)
		b.answerCallback(q.ID, "This menu belongs to another user.")
		return
	}

	if action.menu {
		b.answerCallback(q.ID, "")
		b.sendFolderMenu(ctx, callbackChatID(q), action.userID, b.currentFolderIntro(action.userID))
		return
	}

	folder, err := b.selectFolder(ctx, action)
	if err != nil {
		log.Printf("telegram: select folder for user %d: %v", action.userID, err)
		b.answerCallback(q.ID, "Could not select the folder.")
		b.respondToCallback(q, selectFolderFailedText(err))
		return
	}

	name := folderLabel(folder)
	b.setFolderName(action.userID, name)
	b.answerCallback(q.ID, "Folder selected: "+name)
	b.respondToCallback(q, fmt.Sprintf("✅ Folder selected: %s\n\nYou can now send a business card.", name))
}

// selectFolder selects the chosen folder for its user, resolving index
// choices against the user's current folder list.
func (b *Bot) selectFolder(ctx context.Context, action folderAction) (model.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, folderCallTimeout)
	defer cancel()

	folderID := action.folderID
	if folderID == "" {
		folders, err := b.folders.ListFolders(ctx, action.userID)
		if err != nil {
			return model.Folder{}, err
		}
		if action.index >= len(folders) {
			return model.Folder{}, client.ErrUnknownFolder
		}
		folderID = folders[action.index].ID
	}

	return b.folders.SelectFolder(ctx, action.userID, folderID)
}

// callbackChatID is the chat a callback came from; in private chats it is
// the user's ID.
func callbackChatID(q *tgbotapi.CallbackQuery) int64 {
	if q.Message != nil && q.Message.Chat != nil {
		return q.Message.Chat.ID
	}

	return q.From.ID
}

// respondToCallback replaces the message holding the pressed keyboard with
// text, or sends text if that message is not available.
func (b *Bot) respondToCallback(q *tgbotapi.CallbackQuery, text string) {
	if q.Message == nil || q.Message.Chat == nil {
		b.reply(q.From.ID, text)
		return
	}

	edit := tgbotapi.NewEditMessageText(q.Message.Chat.ID, q.Message.MessageID, text)
	if _, err := b.api.Request(edit); err != nil {
		log.Printf("telegram: failed to edit folder menu: %v", err)
		b.reply(q.Message.Chat.ID, text)
	}
}

func (b *Bot) answerCallback(id, text string) {
	if _, err := b.api.Request(tgbotapi.NewCallback(id, text)); err != nil {
		log.Printf("telegram: failed to answer callback: %v", err)
	}
}

func listFoldersFailedText(err error) string {
	switch {
	case errors.Is(err, client.ErrNotPaired):
		return notConnectedText
	case errors.Is(err, client.ErrAgentOffline):
		return agentOfflineText
	}

	return "❌ Could not load your folders\nReason: " + failureReason(err) + "\nSend /folder to try again."
}

func selectFolderFailedText(err error) string {
	switch {
	case errors.Is(err, client.ErrNotPaired):
		return notConnectedText
	case errors.Is(err, client.ErrAgentOffline):
		return agentOfflineText
	case errors.Is(err, client.ErrUnknownFolder):
		return "❌ This folder is no longer available.\nSend /folder to choose another one."
	}

	return "❌ Could not select the folder\nReason: " + failureReason(err) + "\nSend /folder to try again."
}

// folderName returns the name of the folder the user last selected through
// this bot, or "" if unknown.
func (b *Bot) folderName(userID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.folderNames[userID]
}

// setFolderName remembers the name of the user's selected folder; ""
// forgets it.
func (b *Bot) setFolderName(userID int64, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if name == "" {
		delete(b.folderNames, userID)
		return
	}
	if b.folderNames == nil {
		b.folderNames = make(map[int64]string)
	}
	b.folderNames[userID] = name
}
