package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"obsidian-agent/client"
)

// pairingCallTimeout bounds each pair_agent / pairing check call.
const pairingCallTimeout = 30 * time.Second

// Brute-force protection: a Telegram user may send at most
// maxFailedPairingAttempts wrong codes per failedPairingWindow.
const (
	maxFailedPairingAttempts = 5
	failedPairingWindow      = 10 * time.Minute
)

// pairer is the subset of client.PairingClient the bot uses to pair users
// with the local agent that shows them a pairing code.
type pairer interface {
	PairAgent(ctx context.Context, telegramUserID int64, pairingCode string) (agentID string, err error)
	IsPaired(ctx context.Context, telegramUserID int64) (bool, error)
}

// pairingCodePattern matches what a user may type for an agent code such as
// BUCU-EU: the server's alphabet (no 0, 1, I or O), any case, with an
// optional dash or space. Other text is treated as contact data.
var pairingCodePattern = regexp.MustCompile(`^(?i)[A-HJ-NP-Z2-9]{4}[- ]?[A-HJ-NP-Z2-9]{2}$`)

func looksLikePairingCode(text string) bool {
	return pairingCodePattern.MatchString(strings.TrimSpace(text))
}

const (
	sendPairingCodeText  = "Obsidian agentingizni ulash uchun local-agent terminalida ko‘rsatilgan pairing code'ni shu yerga yuboring."
	pairedText           = "✅ Obsidian connected successfully."
	alreadyPairedText    = "✅ Your Obsidian agent is already connected.\nSend a contact or a business-card photo to save it, or use /folder to change the folder."
	invalidPairingText   = "❌ Invalid or expired pairing code. Check the code shown in your local-agent terminal."
	pairingFailedText    = "❌ Could not connect your Obsidian agent. Please try again later."
	pairingRateLimitText = "⏳ Too many failed pairing attempts. Please try again in %d min."
)

// handleStart tells a paired user how to use the bot and an unpaired one to
// send the pairing code shown by their local agent.
func (b *Bot) handleStart(ctx context.Context, msg *tgbotapi.Message) {
	if msg.From == nil {
		b.reply(msg.Chat.ID, sendPairingCodeText)
		return
	}

	paired, err := b.isPaired(ctx, msg.From.ID)
	if err != nil {
		log.Printf("telegram: check pairing of user %d: %v", msg.From.ID, err)
	}
	if paired {
		b.reply(msg.Chat.ID, usageMessage)
		return
	}

	b.reply(msg.Chat.ID, sendPairingCodeText)
}

// handlePairingCode pairs an unpaired sender with the agent showing code. A
// paired sender keeps their current agent.
func (b *Bot) handlePairingCode(ctx context.Context, msg *tgbotapi.Message, code string) {
	userID := msg.From.ID

	if ok, retryAfter := b.pairingLimiter.allow(userID); !ok {
		b.reply(msg.Chat.ID, fmt.Sprintf(pairingRateLimitText, int(math.Ceil(retryAfter.Minutes()))))
		return
	}

	paired, err := b.isPaired(ctx, userID)
	if err != nil {
		log.Printf("telegram: check pairing of user %d: %v", userID, err)
		b.reply(msg.Chat.ID, pairingFailedText)
		return
	}
	if paired {
		b.reply(msg.Chat.ID, alreadyPairedText)
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, pairingCallTimeout)
	agentID, err := b.pairer.PairAgent(callCtx, userID, code)
	cancel()
	if err != nil {
		// The error never contains the code, so it is safe to log.
		log.Printf("telegram: pair user %d: %v", userID, err)
		if isRejectedPairingCode(err) {
			b.pairingLimiter.fail(userID)
			b.reply(msg.Chat.ID, invalidPairingText)
			return
		}
		b.reply(msg.Chat.ID, pairingFailedText)
		return
	}

	b.pairingLimiter.reset(userID)
	b.setPairedAgent(userID, agentID)
	b.setFolderName(userID, "")
	log.Printf("telegram: user %d paired with agent %s", userID, agentID)

	if b.folders != nil {
		b.sendFolderMenu(ctx, msg.Chat.ID, userID, pairedText)
		return
	}
	b.reply(msg.Chat.ID, pairedText+"\n\n"+usageMessage)
}

// isRejectedPairingCode reports whether the server refused the code itself,
// which counts as a failed attempt; other errors are not the user's fault.
func isRejectedPairingCode(err error) bool {
	return errors.Is(err, client.ErrInvalidPairingCode) ||
		errors.Is(err, client.ErrExpiredPairingCode) ||
		errors.Is(err, client.ErrUsedPairingCode)
}

// isPaired reports whether a user is paired, from what this bot has seen or
// else by asking the MCP server, which owns the user ↔ agent mapping.
func (b *Bot) isPaired(ctx context.Context, userID int64) (bool, error) {
	if b.knownPaired(userID) {
		return true, nil
	}

	ctx, cancel := context.WithTimeout(ctx, pairingCallTimeout)
	defer cancel()

	paired, err := b.pairer.IsPaired(ctx, userID)
	if err != nil {
		return false, err
	}
	if paired {
		b.setPairedAgent(userID, "")
	}
	return paired, nil
}

// knownPaired reports whether this bot saw the user paired.
func (b *Bot) knownPaired(userID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, ok := b.pairedAgents[userID]
	return ok
}

// setPairedAgent remembers that the user is paired, with agentID if known.
func (b *Bot) setPairedAgent(userID int64, agentID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.pairedAgents == nil {
		b.pairedAgents = make(map[int64]string)
	}
	b.pairedAgents[userID] = agentID
}

// forgetPaired drops the bot's record of a pairing the server no longer has.
func (b *Bot) forgetPaired(userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.pairedAgents, userID)
}

// attemptLimiter counts failed pairing attempts per Telegram user in a
// sliding window. The zero value uses the default limits.
type attemptLimiter struct {
	now    func() time.Time // replaceable in tests
	max    int
	window time.Duration

	mu       sync.Mutex
	failures map[int64][]time.Time // user → times of failed attempts within the window
}

func (l *attemptLimiter) limits() (time.Time, int, time.Duration) {
	now, max, window := time.Now(), l.max, l.window
	if l.now != nil {
		now = l.now()
	}
	if max == 0 {
		max = maxFailedPairingAttempts
	}
	if window == 0 {
		window = failedPairingWindow
	}
	return now, max, window
}

// allow reports whether the user may try a code now and, if not, how long
// until they may.
func (l *attemptLimiter) allow(userID int64) (bool, time.Duration) {
	now, max, window := l.limits()

	l.mu.Lock()
	defer l.mu.Unlock()

	recent := l.prune(userID, now, window)
	if len(recent) < max {
		return true, 0
	}
	return false, recent[len(recent)-max].Add(window).Sub(now)
}

// fail records a failed attempt.
func (l *attemptLimiter) fail(userID int64) {
	now, _, window := l.limits()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.failures == nil {
		l.failures = make(map[int64][]time.Time)
	}
	// Sweep users whose failures have all aged out so the map stays small.
	for id := range l.failures {
		l.prune(id, now, window)
	}
	l.failures[userID] = append(l.failures[userID], now)
}

// reset forgets the user's failed attempts.
func (l *attemptLimiter) reset(userID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.failures, userID)
}

// prune drops failures older than window and returns the rest. l.mu must be
// held.
func (l *attemptLimiter) prune(userID int64, now time.Time, window time.Duration) []time.Time {
	times := l.failures[userID]
	i := 0
	for i < len(times) && !now.Before(times[i].Add(window)) {
		i++
	}
	times = times[i:]
	if len(times) == 0 {
		delete(l.failures, userID)
		return nil
	}
	l.failures[userID] = times
	return times
}
