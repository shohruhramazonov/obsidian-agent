package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (f *fakeTelegram) setMyCommands() []tgbotapi.SetMyCommandsConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgbotapi.SetMyCommandsConfig
	for _, c := range f.sent {
		if m, ok := c.(tgbotapi.SetMyCommandsConfig); ok {
			out = append(out, m)
		}
	}
	return out
}

// runUntilStopped runs the bot with an already cancelled context, so Run
// only does its startup work.
func runUntilStopped(t *testing.T, b *Bot) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want context.Canceled", err)
	}
}

func TestRunRegistersCommandsOnce(t *testing.T) {
	b, tg, _ := newTestBot(nil)

	runUntilStopped(t, b)

	calls := tg.setMyCommands()
	if len(calls) != 1 {
		t.Fatalf("setMyCommands called %d times, want 1", len(calls))
	}

	want := map[string]string{
		"start":  "Botni boshlash / Obsidian ulash",
		"folder": "Obsidian papkasini almashtirish",
		"help":   "Botdan foydalanish bo‘yicha yordam",
	}
	got := calls[0].Commands
	if len(got) != len(want) {
		t.Fatalf("registered %d commands, want %d: %v", len(got), len(want), got)
	}
	for _, c := range got {
		if want[c.Command] != c.Description {
			t.Errorf("command %q description = %q, want %q", c.Command, c.Description, want[c.Command])
		}
	}
	if calls[0].Scope != nil || calls[0].LanguageCode != "" {
		t.Errorf("commands must use the default scope and language: %+v", calls[0])
	}
}

func TestRunSurvivesCommandRegistrationFailure(t *testing.T) {
	b, tg, _ := newTestBot(nil)
	tg.requestErr = errors.New("telegram is down")

	runUntilStopped(t, b)

	if len(tg.setMyCommands()) != 1 {
		t.Error("registration was not attempted")
	}
	if len(tg.messages()) != 0 {
		t.Errorf("registration failure must not message users: %v", tg.texts())
	}
}

func TestHelpCommand(t *testing.T) {
	for name, b := range map[string]func() (*Bot, *fakeTelegram){
		"pairing enabled": func() (*Bot, *fakeTelegram) {
			b, tg, _ := newPairingBot(&fakeFolders{folders: standardFolders})
			return b, tg
		},
		"pairing disabled": func() (*Bot, *fakeTelegram) {
			b, tg, _ := newTestBot(nil)
			return b, tg
		},
	} {
		t.Run(name, func(t *testing.T) {
			bot, tg := b()

			bot.handleUpdate(context.Background(), commandUpdate(alice, "/help"))

			if m := onlyMessage(t, tg); m.Text != helpText {
				t.Errorf("text = %q", m.Text)
			}
		})
	}
}

func TestHelpTextCoversFeatures(t *testing.T) {
	for _, s := range []string{"Business card", "/folder", "/start", "Pairing code", "terminal"} {
		if !strings.Contains(strings.ToLower(helpText), strings.ToLower(s)) {
			t.Errorf("help text does not mention %q", s)
		}
	}
}
