package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/client"
)

// fakePairer mimics pair_agent: it pairs a user with agent-1 when they send
// validCode (in any case, with or without the dash), rejects
// expiredCode as expired and anything else as invalid.
type fakePairer struct {
	mu        sync.Mutex
	paired    map[int64]string
	pairCalls int
	codes     []string // codes as received by PairAgent
	checkErr  error
	pairErr   error // returned by PairAgent instead of the rules above
}

const (
	validCode   = "BUCU-EU"
	expiredCode = "EXPR-ED"
)

func (f *fakePairer) PairAgent(ctx context.Context, telegramUserID int64, code string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pairCalls++
	f.codes = append(f.codes, code)
	if f.pairErr != nil {
		return "", f.pairErr
	}
	switch strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code)) {
	case "BUCUEU":
		if f.paired == nil {
			f.paired = make(map[int64]string)
		}
		f.paired[telegramUserID] = "agent-1"
		return "agent-1", nil
	case "EXPRED":
		return "", client.ErrExpiredPairingCode
	default:
		return "", client.ErrInvalidPairingCode
	}
}

func (f *fakePairer) IsPaired(ctx context.Context, telegramUserID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.checkErr != nil {
		return false, f.checkErr
	}
	_, ok := f.paired[telegramUserID]
	return ok, nil
}

func newPairingBot(f *fakeFolders) (*Bot, *fakeTelegram, *fakePairer) {
	b, tg, _ := newTestBot(f)
	p := &fakePairer{}
	b.EnablePairing(p)
	return b, tg, p
}

func textUpdate(from int64, text string) tgbotapi.Update {
	return tgbotapi.Update{Message: &tgbotapi.Message{
		From: &tgbotapi.User{ID: from},
		Chat: &tgbotapi.Chat{ID: from},
		Text: text,
	}}
}

func TestLooksLikePairingCode(t *testing.T) {
	for _, s := range []string{"BUCU-EU", "bucu-eu", "BuCu-Eu", " BUCU-EU\n", "BUCUEU", "bucu eu", "A7K9-42"} {
		if !looksLikePairingCode(s) {
			t.Errorf("looksLikePairingCode(%q) = false", s)
		}
	}
	for _, s := range []string{"", "Google", "BUCU-EUX", "BUC-UEU", "BUCU--EU", "John Smith, Google", "OOOO-00", "A7K9-42MX"} {
		if looksLikePairingCode(s) {
			t.Errorf("looksLikePairingCode(%q) = true", s)
		}
	}
}

func TestStartAsksUnpairedUserForCode(t *testing.T) {
	b, tg, p := newPairingBot(&fakeFolders{folders: standardFolders})

	b.handleUpdate(context.Background(), commandUpdate(alice, "/start"))

	if m := onlyMessage(t, tg); m.Text != sendPairingCodeText {
		t.Errorf("text = %q", m.Text)
	}
	if p.pairCalls != 0 {
		t.Error("/start must not pair")
	}
	for _, text := range tg.texts() {
		if strings.Contains(text, "PAIRING_CODE") {
			t.Errorf("/start still mentions PAIRING_CODE: %q", text)
		}
	}
}

func TestStartForPairedUser(t *testing.T) {
	b, tg, p := newPairingBot(&fakeFolders{folders: standardFolders})
	p.paired = map[int64]string{alice: "agent-1"}

	b.handleUpdate(context.Background(), commandUpdate(alice, "/start"))

	if m := onlyMessage(t, tg); m.Text != usageMessage {
		t.Errorf("text = %q", m.Text)
	}
}

func TestPairingValidCode(t *testing.T) {
	b, tg, p := newPairingBot(&fakeFolders{folders: standardFolders})
	b.setFolderName(alice, "Old")

	b.handleUpdate(context.Background(), textUpdate(alice, "bucu-eu"))

	if p.pairCalls != 1 || p.codes[0] != "bucu-eu" {
		t.Fatalf("PairAgent calls = %d, codes %v; want the raw code once", p.pairCalls, p.codes)
	}
	m := onlyMessage(t, tg)
	if m.Text != "✅ Obsidian connected successfully.\n\nChoose where to save contacts:" {
		t.Errorf("text = %q", m.Text)
	}
	if k := keyboardOf(t, m); len(k.InlineKeyboard) != 4 {
		t.Errorf("keyboard has %d rows, want 4", len(k.InlineKeyboard))
	}
	if !b.knownPaired(alice) || b.pairedAgents[alice] != "agent-1" {
		t.Errorf("paired agents = %v", b.pairedAgents)
	}
	if b.folderName(alice) != "" {
		t.Error("a new pairing must forget the old folder")
	}
	if a := b.agent.(*fakeAgent); a.textInput != "" {
		t.Error("a pairing code must not be turned into a contact")
	}
}

func TestPairingValidCodeWithoutFolders(t *testing.T) {
	b, tg, _ := newPairingBot(nil)

	b.handleUpdate(context.Background(), textUpdate(alice, validCode))

	if m := onlyMessage(t, tg); m.Text != pairedText+"\n\n"+usageMessage {
		t.Errorf("text = %q", m.Text)
	}
}

func TestPairingRejectedCodes(t *testing.T) {
	for _, code := range []string{"AAAA-AA", expiredCode} {
		b, tg, _ := newPairingBot(&fakeFolders{folders: standardFolders})

		b.handleUpdate(context.Background(), textUpdate(alice, code))

		m := onlyMessage(t, tg)
		if m.Text != "❌ Invalid or expired pairing code. Check the code shown in your local-agent terminal." || m.ReplyMarkup != nil {
			t.Errorf("%s: reply = %q, markup %v", code, m.Text, m.ReplyMarkup)
		}
		if b.knownPaired(alice) {
			t.Errorf("%s: user marked paired", code)
		}
	}
}

func TestPairingUsedCodeCountsAsInvalid(t *testing.T) {
	b, tg, p := newPairingBot(nil)
	p.pairErr = client.ErrUsedPairingCode

	b.handleUpdate(context.Background(), textUpdate(alice, validCode))

	if m := onlyMessage(t, tg); m.Text != invalidPairingText {
		t.Errorf("text = %q", m.Text)
	}
}

func TestPairingServerErrorIsNotAFailedAttempt(t *testing.T) {
	b, tg, p := newPairingBot(nil)
	p.pairErr = errors.New("pair_agent: connection refused to http://127.0.0.1:8080")

	for range maxFailedPairingAttempts + 1 {
		b.handleUpdate(context.Background(), textUpdate(alice, validCode))
	}

	for _, text := range tg.texts() {
		if text != pairingFailedText {
			t.Errorf("reply = %q, want %q", text, pairingFailedText)
		}
		assertNoLeaks(t, text)
	}
	if p.pairCalls != maxFailedPairingAttempts+1 {
		t.Errorf("PairAgent calls = %d; server errors must not lock the user out", p.pairCalls)
	}
}

func TestPairingAlreadyPairedUser(t *testing.T) {
	b, tg, p := newPairingBot(&fakeFolders{folders: standardFolders})
	p.paired = map[int64]string{alice: "agent-1"} // e.g. paired before a bot restart

	b.handleUpdate(context.Background(), textUpdate(alice, "NEWA-GT"))

	if m := onlyMessage(t, tg); m.Text != alreadyPairedText {
		t.Errorf("text = %q", m.Text)
	}
	if p.pairCalls != 0 || p.paired[alice] != "agent-1" {
		t.Errorf("PairAgent calls = %d, pairing %q; the existing pairing must be kept", p.pairCalls, p.paired[alice])
	}

	// Once known, the bot does not ask the server again.
	p.checkErr = errors.New("must not be called")
	tg.sent = nil
	b.handleUpdate(context.Background(), textUpdate(alice, validCode))
	if m := onlyMessage(t, tg); m.Text != alreadyPairedText || p.pairCalls != 0 {
		t.Errorf("text = %q, PairAgent calls = %d", m.Text, p.pairCalls)
	}
}

func TestPairingCheckFailure(t *testing.T) {
	b, tg, p := newPairingBot(nil)
	p.checkErr = errors.New("list_folders: connection refused")

	b.handleUpdate(context.Background(), textUpdate(alice, validCode))

	if m := onlyMessage(t, tg); m.Text != pairingFailedText {
		t.Errorf("text = %q", m.Text)
	}
	if p.pairCalls != 0 {
		t.Error("must not pair when the pairing state is unknown")
	}
}

func TestPairingRateLimit(t *testing.T) {
	b, tg, p := newPairingBot(nil)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	b.pairingLimiter.now = func() time.Time { return now }

	for range maxFailedPairingAttempts {
		b.handleUpdate(context.Background(), textUpdate(alice, "AAAA-AA"))
		now = now.Add(time.Minute)
	}
	if p.pairCalls != maxFailedPairingAttempts {
		t.Fatalf("PairAgent calls = %d", p.pairCalls)
	}

	// Blocked, even for the right code, until the first failure ages out.
	tg.sent = nil
	b.handleUpdate(context.Background(), textUpdate(alice, validCode))
	if m := onlyMessage(t, tg); m.Text != "⏳ Too many failed pairing attempts. Please try again in 5 min." {
		t.Errorf("text = %q", m.Text)
	}
	if p.pairCalls != maxFailedPairingAttempts {
		t.Error("blocked attempt reached the server")
	}

	// Other users are not affected.
	tg.sent = nil
	b.handleUpdate(context.Background(), textUpdate(alice+1, validCode))
	if m := onlyMessage(t, tg); m.Text != pairedText+"\n\n"+usageMessage {
		t.Errorf("other user: text = %q", m.Text)
	}

	// Ten minutes after the first failure one attempt is available again.
	now = now.Add(5 * time.Minute)
	tg.sent = nil
	b.handleUpdate(context.Background(), textUpdate(alice, validCode))
	if m := onlyMessage(t, tg); m.Text != pairedText+"\n\n"+usageMessage {
		t.Errorf("after window: text = %q", m.Text)
	}
}

func TestSuccessfulPairingResetsFailedAttempts(t *testing.T) {
	b, _, p := newPairingBot(nil)

	for range maxFailedPairingAttempts - 1 {
		b.handleUpdate(context.Background(), textUpdate(alice, "AAAA-AA"))
	}
	b.handleUpdate(context.Background(), textUpdate(alice, validCode))

	if _, ok := b.pairingLimiter.failures[alice]; ok {
		t.Errorf("failures after pairing = %v", b.pairingLimiter.failures[alice])
	}

	// Unpaired again (e.g. the agent was paired to someone else): a full
	// set of attempts is available.
	delete(p.paired, alice)
	b.forgetPaired(alice)
	for range maxFailedPairingAttempts - 1 {
		b.handleUpdate(context.Background(), textUpdate(alice, "AAAA-AA"))
	}
	if ok, _ := b.pairingLimiter.allow(alice); !ok {
		t.Error("user blocked although the counter was reset by the successful pairing")
	}
}

func TestAttemptLimiterSweepsOldUsers(t *testing.T) {
	now := time.Now()
	l := attemptLimiter{now: func() time.Time { return now }}

	l.fail(1)
	now = now.Add(failedPairingWindow)
	l.fail(2)

	if _, ok := l.failures[1]; ok || len(l.failures) != 1 {
		t.Errorf("failures = %v, want only user 2", l.failures)
	}
}

func TestPairingDisabledKeepsContactFlow(t *testing.T) {
	b, tg, s := newTestBot(&fakeFolders{folders: standardFolders})
	a := b.agent.(*fakeAgent)

	b.handleUpdate(context.Background(), textUpdate(alice, validCode))

	if a.textInput != validCode || s.calls != 1 || len(tg.messages()) != 1 {
		t.Errorf("without pairing a code-like text is contact data: input %q, saves %d", a.textInput, s.calls)
	}
}

func TestBusinessCardFlowUnchangedWithPairing(t *testing.T) {
	b, tg, p := newPairingBot(&fakeFolders{folders: standardFolders})
	s := b.saver.(*fakeSaver)
	a := b.agent.(*fakeAgent)
	b.setFolderName(alice, "Contacts")

	b.handleUpdate(context.Background(), textUpdate(alice, "Jerry M. Chen, Google"))

	if a.textInput != "Jerry M. Chen, Google" {
		t.Errorf("CreateContact input = %q", a.textInput)
	}
	if p.pairCalls != 0 {
		t.Error("contact text was sent to pair_agent")
	}
	assertSavedAndConfirmed(t, tg, s)
}

func TestNotPairedSaveForgetsPairing(t *testing.T) {
	b, tg, _ := newPairingBot(&fakeFolders{folders: standardFolders})
	b.saver.(*fakeSaver).err = client.ErrNotPaired
	b.setPairedAgent(alice, "agent-1")

	b.handleUpdate(context.Background(), textUpdate(alice, "Jerry M. Chen, Google"))

	if m := onlyMessage(t, tg); m.Text != notConnectedText {
		t.Errorf("text = %q", m.Text)
	}
	if b.knownPaired(alice) {
		t.Error("pairing kept after the server reported the user unpaired")
	}
}
