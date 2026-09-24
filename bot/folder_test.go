package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/agent"
	"obsidian-agent/client"
	"obsidian-agent/model"
)

// fakeTelegram records everything the bot sends to Telegram.
type fakeTelegram struct {
	mu         sync.Mutex
	sent       []tgbotapi.Chattable
	fileURL    string
	requestErr error // returned by Request, if set
}

func (f *fakeTelegram) GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel { return nil }
func (f *fakeTelegram) StopReceivingUpdates()                                        {}
func (f *fakeTelegram) GetFileDirectURL(string) (string, error)                      { return f.fileURL, nil }

func (f *fakeTelegram) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	return tgbotapi.Message{}, nil
}

func (f *fakeTelegram) Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	if f.requestErr != nil {
		return nil, f.requestErr
	}
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (f *fakeTelegram) messages() []tgbotapi.MessageConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgbotapi.MessageConfig
	for _, c := range f.sent {
		if m, ok := c.(tgbotapi.MessageConfig); ok {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeTelegram) callbackAnswers() []tgbotapi.CallbackConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgbotapi.CallbackConfig
	for _, c := range f.sent {
		if a, ok := c.(tgbotapi.CallbackConfig); ok {
			out = append(out, a)
		}
	}
	return out
}

func (f *fakeTelegram) edits() []tgbotapi.EditMessageTextConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgbotapi.EditMessageTextConfig
	for _, c := range f.sent {
		if e, ok := c.(tgbotapi.EditMessageTextConfig); ok {
			out = append(out, e)
		}
	}
	return out
}

// texts returns the text of every message and edit sent.
func (f *fakeTelegram) texts() []string {
	var out []string
	for _, m := range f.messages() {
		out = append(out, m.Text)
	}
	for _, e := range f.edits() {
		out = append(out, e.Text)
	}
	return out
}

// fakeFolders is a folderSelector with a fixed folder list.
type fakeFolders struct {
	folders   []model.Folder
	listErr   error
	selectErr error

	selectCalls    int
	selectedUser   int64
	selectedFolder string
}

func (f *fakeFolders) ListFolders(ctx context.Context, telegramUserID int64) ([]model.Folder, error) {
	return f.folders, f.listErr
}

func (f *fakeFolders) SelectFolder(ctx context.Context, telegramUserID int64, folderID string) (model.Folder, error) {
	f.selectCalls++
	f.selectedUser, f.selectedFolder = telegramUserID, folderID
	if f.selectErr != nil {
		return model.Folder{}, f.selectErr
	}
	for _, folder := range f.folders {
		if folder.ID == folderID {
			return folder, nil
		}
	}
	return model.Folder{}, client.ErrUnknownFolder
}

// fakeAgent returns jerry for any input.
type fakeAgent struct {
	textInput string
	imageSeen bool
}

func (f *fakeAgent) CreateContact(ctx context.Context, input string) (*model.Contact, error) {
	f.textInput = input
	return jerry, nil
}

func (f *fakeAgent) CreateContactFromImage(ctx context.Context, imagePath string) (*model.Contact, error) {
	_, err := os.Stat(imagePath)
	f.imageSeen = err == nil
	return jerry, nil
}

var standardFolders = []model.Folder{
	{ID: "contacts", Name: "Contacts", Path: "Contacts"},
	{ID: "people", Name: "People", Path: "People"},
	{ID: "companies", Name: "Companies", Path: "Companies"},
	{ID: "projects", Name: "Projects", Path: "Projects"},
}

const alice int64 = 4242

func newTestBot(f *fakeFolders) (*Bot, *fakeTelegram, *fakeSaver) {
	tg := &fakeTelegram{}
	s := &fakeSaver{}
	b := &Bot{api: tg, agent: &fakeAgent{}, saver: s}
	if f != nil {
		b.EnableFolders(f)
	}
	return b, tg, s
}

func commandUpdate(from int64, text string) tgbotapi.Update {
	return tgbotapi.Update{Message: &tgbotapi.Message{
		From:     &tgbotapi.User{ID: from},
		Chat:     &tgbotapi.Chat{ID: from},
		Text:     text,
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(text)}},
	}}
}

func callbackUpdate(from int64, data string) tgbotapi.Update {
	return tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "cb-1",
		From:    &tgbotapi.User{ID: from},
		Message: &tgbotapi.Message{MessageID: 7, Chat: &tgbotapi.Chat{ID: from}},
		Data:    data,
	}}
}

func keyboardOf(t *testing.T, m tgbotapi.MessageConfig) tgbotapi.InlineKeyboardMarkup {
	t.Helper()
	k, ok := m.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("message %q has no inline keyboard", m.Text)
	}
	return k
}

func onlyMessage(t *testing.T, tg *fakeTelegram) tgbotapi.MessageConfig {
	t.Helper()
	msgs := tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1: %v", len(msgs), tg.texts())
	}
	return msgs[0]
}

func TestFolderKeyboard(t *testing.T) {
	long := model.Folder{ID: strings.Repeat("x", 80), Name: "Clients", Path: "Clients"}
	noName := model.Folder{ID: "f-123", Path: "Archive/2025"}
	folders := append(append([]model.Folder{}, standardFolders...), long, noName)

	k := folderKeyboard(alice, folders)

	want := []struct{ text, data string }{
		{"Contacts", "folder:4242:contacts"},
		{"People", "folder:4242:people"},
		{"Companies", "folder:4242:companies"},
		{"Projects", "folder:4242:projects"},
		{"Clients", "folder#:4242:4"},
		{"Archive/2025", "folder:4242:f-123"}, // label never shows the internal id
	}
	if len(k.InlineKeyboard) != len(want) {
		t.Fatalf("got %d rows, want %d", len(k.InlineKeyboard), len(want))
	}
	for i, w := range want {
		row := k.InlineKeyboard[i]
		if len(row) != 1 || row[0].Text != w.text || row[0].CallbackData == nil || *row[0].CallbackData != w.data {
			t.Errorf("row %d = %+v, want %q -> %q", i, row, w.text, w.data)
		}
		if len(*row[0].CallbackData) > maxCallbackDataLen {
			t.Errorf("row %d callback data exceeds %d bytes", i, maxCallbackDataLen)
		}
	}
}

func TestParseFolderCallback(t *testing.T) {
	valid := map[string]folderAction{
		"folder:4242:contacts":   {userID: 4242, folderID: "contacts"},
		"folder:4242:team:notes": {userID: 4242, folderID: "team:notes"},
		"folder#:4242:2":         {userID: 4242, index: 2},
		"folders:4242":           {userID: 4242, menu: true},
	}
	for data, want := range valid {
		if got, ok := parseFolderCallback(data); !ok || got != want {
			t.Errorf("parseFolderCallback(%q) = %+v, %v; want %+v", data, got, ok, want)
		}
	}

	invalid := []string{
		"", "folder:", "folder:4242:", "folder:abc:contacts", "folder:0:contacts",
		"folder#:4242:x", "folder#:4242:-1", "folders:", "folders:abc",
		"folder:contacts", // no user binding
		"vault:4242:work", // old vault buttons are no longer accepted
	}
	for _, data := range invalid {
		if _, ok := parseFolderCallback(data); ok {
			t.Errorf("parseFolderCallback(%q) accepted", data)
		}
	}
}

func TestFolderCommand(t *testing.T) {
	b, tg, _ := newTestBot(&fakeFolders{folders: standardFolders})

	b.handleUpdate(context.Background(), commandUpdate(alice, "/folder"))

	m := onlyMessage(t, tg)
	if m.Text != chooseFolderText {
		t.Errorf("text = %q", m.Text)
	}
	if k := keyboardOf(t, m); *k.InlineKeyboard[0][0].CallbackData != "folder:4242:contacts" {
		t.Errorf("first button = %+v", k.InlineKeyboard[0][0])
	}
}

func TestFolderCommandShowsCurrentFolder(t *testing.T) {
	b, tg, _ := newTestBot(&fakeFolders{folders: standardFolders})
	b.setFolderName(alice, "Contacts")

	b.handleUpdate(context.Background(), commandUpdate(alice, "/folder"))

	if m := onlyMessage(t, tg); m.Text != "📁 Current folder: Contacts\n\n"+chooseFolderText {
		t.Errorf("text = %q", m.Text)
	}
}

func TestFolderCommandErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"not paired", client.ErrNotPaired, notConnectedText},
		{"agent disconnected", client.ErrAgentOffline, agentOfflineText},
		{"agent timeout", client.ErrAgentTimeout, "❌ Could not load your folders\nReason: your Obsidian agent did not respond in time\nSend /folder to try again."},
		{"mcp timeout", context.DeadlineExceeded, "❌ Could not load your folders\nReason: your Obsidian agent did not respond in time\nSend /folder to try again."},
		{"mcp connection error", errors.New(`list_folders: Post "http://127.0.0.1:8080/mcp": dial tcp: connection refused Bearer secret-key goroutine 1`), "❌ Could not load your folders\nReason: unexpected error, please try again later\nSend /folder to try again."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, tg, _ := newTestBot(&fakeFolders{listErr: tt.err})

			b.handleUpdate(context.Background(), commandUpdate(alice, "/folder"))

			m := onlyMessage(t, tg)
			if m.Text != tt.want {
				t.Errorf("text = %q, want %q", m.Text, tt.want)
			}
			if m.ReplyMarkup != nil {
				t.Error("error reply must not have a keyboard")
			}
			assertNoLeaks(t, m.Text)
		})
	}
}

func TestFolderCommandWithoutFoldersEnabled(t *testing.T) {
	b, tg, _ := newTestBot(nil)

	b.handleUpdate(context.Background(), commandUpdate(alice, "/folder"))

	if m := onlyMessage(t, tg); m.Text != usageMessage {
		t.Errorf("text = %q", m.Text)
	}
}

func TestOldVaultButtonsIgnored(t *testing.T) {
	f := &fakeFolders{folders: standardFolders}
	b, tg, _ := newTestBot(f)

	b.handleUpdate(context.Background(), callbackUpdate(alice, "vault:4242:work"))

	if f.selectCalls != 0 || len(tg.messages()) != 0 || len(tg.edits()) != 0 {
		t.Errorf("old vault button was acted on: %v", tg.texts())
	}
}

func TestFolderCallbackSelectsFolder(t *testing.T) {
	f := &fakeFolders{folders: standardFolders}
	b, tg, s := newTestBot(f)

	b.handleUpdate(context.Background(), callbackUpdate(alice, "folder:4242:contacts"))

	if f.selectCalls != 1 || f.selectedUser != alice || f.selectedFolder != "contacts" {
		t.Fatalf("SelectFolder calls=%d user=%d folder=%q", f.selectCalls, f.selectedUser, f.selectedFolder)
	}
	answers := tg.callbackAnswers()
	if len(answers) != 1 || answers[0].CallbackQueryID != "cb-1" || answers[0].Text != "Folder selected: Contacts" {
		t.Errorf("callback answers = %+v", answers)
	}
	edits := tg.edits()
	if len(edits) != 1 || edits[0].MessageID != 7 || edits[0].Text != "✅ Folder selected: Contacts\n\nYou can now send a business card." {
		t.Errorf("edits = %+v", edits)
	}

	// Saves now name the selected folder, and save_file still only gets the
	// Telegram user ID and a file name.
	got, _ := b.saveContact(context.Background(), &tgbotapi.User{ID: alice}, jerry)
	if want := "✅ Contact saved to Contacts: Jerry M. Chen"; got != want {
		t.Errorf("save reply = %q, want %q", got, want)
	}
	if s.telegramUserID != alice || s.path != "Jerry M. Chen.md" {
		t.Errorf("SaveFile user=%d path=%q", s.telegramUserID, s.path)
	}
}

func TestFolderCallbackByIndex(t *testing.T) {
	long := model.Folder{ID: strings.Repeat("x", 80), Name: "Clients"}
	f := &fakeFolders{folders: append(append([]model.Folder{}, standardFolders...), long)}
	b, _, _ := newTestBot(f)

	b.handleUpdate(context.Background(), callbackUpdate(alice, "folder#:4242:4"))
	if f.selectedFolder != long.ID {
		t.Errorf("selected %q, want the long folder id", f.selectedFolder)
	}

	f.selectCalls = 0
	b.handleUpdate(context.Background(), callbackUpdate(alice, "folder#:4242:9"))
	if f.selectCalls != 0 {
		t.Error("out-of-range index must not call SelectFolder")
	}
}

func TestFolderCallbackRejectsOtherUser(t *testing.T) {
	for _, data := range []string{"folder:4242:contacts", "folder#:4242:0", "folders:4242"} {
		t.Run(data, func(t *testing.T) {
			f := &fakeFolders{folders: standardFolders}
			b, tg, _ := newTestBot(f)

			// Bob (999) presses a button from Alice's message.
			b.handleUpdate(context.Background(), callbackUpdate(999, data))

			if f.selectCalls != 0 {
				t.Fatalf("SelectFolder called for another user's button (user %d)", f.selectedUser)
			}
			answers := tg.callbackAnswers()
			if len(answers) != 1 || answers[0].Text != "This menu belongs to another user." {
				t.Errorf("callback answers = %+v", answers)
			}
			if len(tg.edits()) != 0 || len(tg.messages()) != 0 {
				t.Errorf("unexpected replies: %v", tg.texts())
			}
			if b.folderName(alice) != "" || b.folderName(999) != "" {
				t.Error("rejected callback changed a remembered folder")
			}
		})
	}
}

func TestFolderCallbackIgnoresUnknownData(t *testing.T) {
	f := &fakeFolders{folders: standardFolders}
	b, tg, _ := newTestBot(f)

	b.handleUpdate(context.Background(), callbackUpdate(alice, "something-else"))

	if f.selectCalls != 0 {
		t.Error("SelectFolder called for unknown callback data")
	}
	if answers := tg.callbackAnswers(); len(answers) != 1 {
		t.Errorf("callback must still be answered, got %+v", answers)
	}
}

func TestFolderCallbackSelectErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"invalid folder", client.ErrUnknownFolder, "❌ This folder is no longer available.\nSend /folder to choose another one."},
		{"not paired", client.ErrNotPaired, notConnectedText},
		{"agent disconnected", client.ErrAgentOffline, agentOfflineText},
		{"internal", errors.New("select_folder failed: ws://10.0.0.5/agent Bearer secret-key goroutine 1 [running]"), "❌ Could not select the folder\nReason: unexpected error, please try again later\nSend /folder to try again."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, tg, _ := newTestBot(&fakeFolders{folders: standardFolders, selectErr: tt.err})

			b.handleUpdate(context.Background(), callbackUpdate(alice, "folder:4242:contacts"))

			if answers := tg.callbackAnswers(); len(answers) != 1 || answers[0].Text != "Could not select the folder." {
				t.Errorf("callback answers = %+v", answers)
			}
			edits := tg.edits()
			if len(edits) != 1 || edits[0].Text != tt.want {
				t.Errorf("edits = %+v, want %q", edits, tt.want)
			}
			if b.folderName(alice) != "" {
				t.Error("failed selection was remembered")
			}
			for _, text := range tg.texts() {
				assertNoLeaks(t, text)
			}
		})
	}
}

func TestNoFolderSelectedOffersButton(t *testing.T) {
	b, tg, s := newTestBot(&fakeFolders{folders: standardFolders})
	s.err = client.ErrNoFolderSelected
	b.setFolderName(alice, "Contacts") // stale after e.g. a server restart

	b.handleUpdate(context.Background(), tgbotapi.Update{Message: &tgbotapi.Message{
		From: &tgbotapi.User{ID: alice},
		Chat: &tgbotapi.Chat{ID: alice},
		Text: "Jerry M. Chen, Google",
	}})

	m := onlyMessage(t, tg)
	if m.Text != "Please choose a folder first." {
		t.Errorf("text = %q", m.Text)
	}
	k := keyboardOf(t, m)
	if len(k.InlineKeyboard) != 1 || *k.InlineKeyboard[0][0].CallbackData != "folders:4242" {
		t.Fatalf("keyboard = %+v", k.InlineKeyboard)
	}
	if b.folderName(alice) != "" {
		t.Error("stale folder name kept after the server reported no selection")
	}

	// Pressing the button shows the folder keyboard.
	tg.sent = nil
	b.handleUpdate(context.Background(), callbackUpdate(alice, "folders:4242"))

	m = onlyMessage(t, tg)
	if m.Text != chooseFolderText || len(keyboardOf(t, m).InlineKeyboard) != 4 {
		t.Errorf("menu = %q, %+v", m.Text, m.ReplyMarkup)
	}
	if answers := tg.callbackAnswers(); len(answers) != 1 {
		t.Errorf("callback answers = %+v", answers)
	}
}

func TestTextFlowSavesContact(t *testing.T) {
	b, tg, s := newTestBot(&fakeFolders{folders: standardFolders})
	a := b.agent.(*fakeAgent)
	b.setFolderName(alice, "Contacts")

	b.handleUpdate(context.Background(), tgbotapi.Update{Message: &tgbotapi.Message{
		From: &tgbotapi.User{ID: alice},
		Chat: &tgbotapi.Chat{ID: alice},
		Text: "Jerry M. Chen, Google",
	}})

	if a.textInput != "Jerry M. Chen, Google" {
		t.Errorf("CreateContact input = %q", a.textInput)
	}
	assertSavedAndConfirmed(t, tg, s)
}

func TestPhotoFlowSavesContact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake jpeg"))
	}))
	defer srv.Close()

	b, tg, s := newTestBot(&fakeFolders{folders: standardFolders})
	tg.fileURL = srv.URL + "/photos/file_1.jpg"
	a := b.agent.(*fakeAgent)
	b.setFolderName(alice, "Contacts")

	b.handleUpdate(context.Background(), tgbotapi.Update{Message: &tgbotapi.Message{
		From:  &tgbotapi.User{ID: alice},
		Chat:  &tgbotapi.Chat{ID: alice},
		Photo: []tgbotapi.PhotoSize{{FileID: "small", Width: 90, Height: 90}, {FileID: "large", Width: 1280, Height: 960}},
	}})

	if !a.imageSeen {
		t.Error("CreateContactFromImage did not get the downloaded photo")
	}
	assertSavedAndConfirmed(t, tg, s)
}

// assertSavedAndConfirmed checks that the contact went to save_file with the
// sender's Telegram user ID and a bare file name, and that Telegram only got
// a short confirmation.
func assertSavedAndConfirmed(t *testing.T, tg *fakeTelegram, s *fakeSaver) {
	t.Helper()

	markdown := agent.GenerateMarkdown(jerry)
	if s.calls != 1 || s.telegramUserID != alice || s.content != markdown {
		t.Errorf("SaveFile calls=%d user=%d, content is Markdown: %v", s.calls, s.telegramUserID, s.content == markdown)
	}
	if s.path != "Jerry M. Chen.md" {
		t.Errorf("SaveFile path = %q; the bot must not choose folders", s.path)
	}

	m := onlyMessage(t, tg)
	if m.Text != "✅ Contact saved to Contacts: Jerry M. Chen" || m.ReplyMarkup != nil {
		t.Errorf("reply = %q, markup %v", m.Text, m.ReplyMarkup)
	}
	for _, c := range tg.sent {
		if _, ok := c.(tgbotapi.DocumentConfig); ok {
			t.Error("bot sent a document; the Markdown must stay in Obsidian")
		}
	}
	for _, text := range tg.texts() {
		if strings.Contains(text, markdown) || strings.Contains(text, "dataview") {
			t.Errorf("bot sent the Markdown to Telegram: %q", text)
		}
	}
}

func assertNoLeaks(t *testing.T, text string) {
	t.Helper()
	for _, leak := range []string{"secret", "http", "ws://", "goroutine", "list_folders", "select_folder", "127.0.0.1", "Bearer"} {
		if strings.Contains(text, leak) {
			t.Errorf("reply leaks %q: %q", leak, text)
		}
	}
}
