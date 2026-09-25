package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	openai "github.com/sashabaranov/go-openai"

	"obsidian-agent/agent"
	"obsidian-agent/bot"
	"obsidian-agent/client"
	"obsidian-agent/model"
)

const openRouterBaseURL = "https://openrouter.ai/api/v1"
const defaultOpenRouterModel = "anthropic/claude-sonnet-4.5"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var imagePath string
	flag.StringVar(&imagePath, "image", "", "path to a business card image to extract a contact from")
	flag.Parse()

	llmModel := os.Getenv("OPENROUTER_MODEL")
	if llmModel == "" {
		llmModel = defaultOpenRouterModel
	}

	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")

	if telegramToken != "" {
		// Telegram mode: the agent only extracts contacts; the bot saves
		// them to each user's vault through the pairing server, so no
		// direct Obsidian connection (and no ContactWriter) is needed.
		openRouterKey := os.Getenv("OPENROUTER_API_KEY")
		if openRouterKey == "" {
			log.Fatal("OPENROUTER_API_KEY is not set")
		}

		pairingURL := os.Getenv("PAIRING_SERVER_URL")
		if pairingURL == "" {
			log.Fatal("PAIRING_SERVER_URL is not set")
		}

		a := agent.New(newLLMClient(openRouterKey), llmModel, nil)

		runBot(ctx, telegramToken, a, pairingURL)
		return
	}

	// CLI mode: the agent writes the contact directly to Obsidian.
	mcpURL := os.Getenv("OBSIDIAN_MCP_URL")
	apiKey := os.Getenv("OBSIDIAN_API_KEY")
	openRouterKey := os.Getenv("OPENROUTER_API_KEY")

	if mcpURL == "" {
		log.Fatal("OBSIDIAN_MCP_URL is not set")
	}
	if apiKey == "" {
		log.Fatal("OBSIDIAN_API_KEY is not set")
	}
	if openRouterKey == "" {
		log.Fatal("OPENROUTER_API_KEY is not set")
	}

	obsidian, err := client.New(ctx, mcpURL, apiKey)
	if err != nil {
		log.Fatal(err)
	}
	defer obsidian.Close()

	a := agent.New(newLLMClient(openRouterKey), llmModel, obsidian)

	runCLI(ctx, a, imagePath)
}

// newLLMClient returns an OpenAI-compatible client pointed at OpenRouter.
func newLLMClient(openRouterKey string) *openai.Client {
	config := openai.DefaultConfig(openRouterKey)
	config.BaseURL = openRouterBaseURL
	return openai.NewClientWithConfig(config)
}

// runBot starts the Telegram bot and blocks until ctx is cancelled. If
// pairingURL is set, users pair with their local agent by sending the bot the
// pairing code shown in the agent's terminal, through the obsidian-mcp server
// at that URL.
func runBot(ctx context.Context, token string, a *agent.Agent, pairingURL string) {
	b, err := bot.New(token, a)
	if err != nil {
		log.Fatal(err)
	}

	if pairingURL != "" {
		p, err := client.NewPairingClient(ctx, pairingURL)
		if err != nil {
			log.Fatal(err)
		}
		defer p.Close()
		b.EnablePairing(p)
		b.EnableSaving(p)
		b.EnableFolders(p)
		log.Printf("pairing enabled via %s", pairingURL)
	}

	log.Println("telegram bot started")

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// runCLI runs the original one-shot CLI flow: extract a single contact from
// an image or from CLI args/stdin, write it to the vault, and print it.
func runCLI(ctx context.Context, a *agent.Agent, imagePath string) {
	var contact *model.Contact
	var err error

	if imagePath != "" {
		contact, err = a.CreateContactFromImage(ctx, imagePath)
	} else {
		var input string
		input, err = readInput()
		if err == nil {
			contact, err = a.CreateContact(ctx, input)
		}
	}
	if err != nil {
		log.Fatal(err)
	}

	path := agent.FilePath(contact.Name)

	fmt.Printf("Created contact: %+v\n", *contact)
	fmt.Printf("Path: %s\n", path)
}

// readInput returns the contact description from the remaining CLI
// arguments, falling back to stdin if none were given.
func readInput() (string, error) {
	if args := flag.Args(); len(args) > 0 {
		return strings.Join(args, " "), nil
	}

	data, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}

	return string(data), nil
}
