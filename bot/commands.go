package bot

import (
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// botCommands is the command menu Telegram shows when the user types "/".
var botCommands = []tgbotapi.BotCommand{
	{Command: "start", Description: "Botni boshlash / Obsidian ulash"},
	{Command: "folder", Description: "Obsidian papkasini almashtirish"},
	{Command: "help", Description: "Botdan foydalanish bo‘yicha yordam"},
}

const helpText = `ℹ️ Botdan foydalanish:

📇 Business card rasmini yuboring — kontakt Obsidian'ga saqlanadi.
📁 /folder — kontaktlar saqlanadigan papkani almashtirish.
🔗 /start — Obsidian'ni ulash.

Pairing code local-agent ishga tushganda uning terminalida ko‘rsatiladi. Uni shu chatga yuboring.`

// registerCommands publishes the command menu to Telegram.
func (b *Bot) registerCommands() error {
	if _, err := b.api.Request(tgbotapi.NewSetMyCommands(botCommands...)); err != nil {
		return fmt.Errorf("set bot commands: %w", err)
	}
	return nil
}

// registerCommandsOrLog registers the command menu, logging failures: the
// bot works without the menu, so they must not stop it from starting.
func (b *Bot) registerCommandsOrLog() {
	if err := b.registerCommands(); err != nil {
		log.Printf("telegram: %v", err)
	}
}
