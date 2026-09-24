package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"obsidian-agent/model"
)

// ContactWriter persists generated markdown content at a given vault path.
// obsidian.ObsidianClient.Write satisfies this interface.
type ContactWriter interface {
	Write(ctx context.Context, path, content string) error
}

const systemPrompt = `You are a contact information extraction engine.
Given information about a person, either as free-form text or as an image
of a business card, extract their contact details.

Respond with ONLY a single valid JSON object with exactly these keys:
"name", "birthday", "phone", "email", "telegram", "linkedin", "company",
"position", "where_worked", "relationship", "category", "projects".

All values must be strings. Extract ONLY information actually present on the
business card. Do not invent or guess missing fields. If a field is not
mentioned or unknown, use an empty string "". Do not include markdown
formatting, code fences, explanations, or any text other than the JSON object.`

const visionUserPrompt = `Extract the contact details visible in this business card image.`

// invalidFilenameChars matches characters that are unsafe to use in
// filenames across common filesystems.
var invalidFilenameChars = strings.NewReplacer(
	"/", "-",
	"\\", "-",
	":", "-",
	"*", "-",
	"?", "-",
	"\"", "-",
	"<", "-",
	">", "-",
	"|", "-",
)

// Agent extracts structured contacts from unstructured input using an LLM.
// The Create* methods also write them to an Obsidian vault via a
// ContactWriter; the Extract* methods only extract and validate.
type Agent struct {
	client *openai.Client
	model  string
	writer ContactWriter
}

// New creates an Agent backed by the given OpenAI-compatible client, model
// name, and ContactWriter.
func New(client *openai.Client, model string, writer ContactWriter) *Agent {
	return &Agent{
		client: client,
		model:  model,
		writer: writer,
	}
}

// CreateContact extracts a Contact from the given free-form input using the
// LLM, renders it as markdown, and writes it to the vault via the Agent's
// ContactWriter. It returns the extracted Contact.
//
// Callers that persist the contact themselves should use ExtractContact
// instead to avoid writing it twice.
func (a *Agent) CreateContact(ctx context.Context, input string) (*model.Contact, error) {
	contact, err := a.ExtractContact(ctx, input)
	if err != nil {
		return nil, err
	}

	if err := a.writeContact(ctx, contact); err != nil {
		return nil, fmt.Errorf("write contact: %w", err)
	}

	return contact, nil
}

// CreateContactFromImage extracts a Contact from a business card image at
// the given path using a vision-capable LLM, renders it as markdown, and
// writes it to the vault via the Agent's ContactWriter. It returns the
// extracted Contact.
//
// Callers that persist the contact themselves should use
// ExtractContactFromImage instead to avoid writing it twice.
func (a *Agent) CreateContactFromImage(ctx context.Context, imagePath string) (*model.Contact, error) {
	contact, err := a.ExtractContactFromImage(ctx, imagePath)
	if err != nil {
		return nil, err
	}

	if err := a.writeContact(ctx, contact); err != nil {
		return nil, fmt.Errorf("write contact: %w", err)
	}

	return contact, nil
}

// ExtractContact extracts and validates a Contact from the given free-form
// input using the LLM. It does not write anything to the vault.
func (a *Agent) ExtractContact(ctx context.Context, input string) (*model.Contact, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("input is empty")
	}

	contact, err := a.extractContact(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("extract contact: %w", err)
	}

	if err := validateContact(contact); err != nil {
		return nil, err
	}

	return contact, nil
}

// ExtractContactFromImage extracts and validates a Contact from a business
// card image at the given path using a vision-capable LLM. It does not
// write anything to the vault.
func (a *Agent) ExtractContactFromImage(ctx context.Context, imagePath string) (*model.Contact, error) {
	dataURL, err := encodeImageDataURL(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}

	contact, err := a.completeContact(ctx, buildImageMessages(dataURL))
	if err != nil {
		return nil, fmt.Errorf("extract contact from image: %w", err)
	}

	if err := validateContact(contact); err != nil {
		return nil, err
	}

	return contact, nil
}

// validateContact checks that an extracted contact has the fields required
// to be saved as a note.
func validateContact(contact *model.Contact) error {
	if strings.TrimSpace(contact.Name) == "" {
		return fmt.Errorf("extracted contact has no name")
	}

	return nil
}

// writeContact renders the contact as markdown and writes it to the vault
// via the Agent's ContactWriter.
func (a *Agent) writeContact(ctx context.Context, c *model.Contact) error {
	markdown := GenerateMarkdown(c)
	path := FilePath(c.Name)

	return a.writer.Write(ctx, path, markdown)
}

func (a *Agent) extractContact(ctx context.Context, input string) (*model.Contact, error) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: input},
	}

	return a.completeContact(ctx, messages)
}

// buildImageMessages builds the chat messages sent to the vision model for
// a business card image, given as a data: URL.
func buildImageMessages(dataURL string) []openai.ChatCompletionMessage {
	return []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{
			Role: openai.ChatMessageRoleUser,
			MultiContent: []openai.ChatMessagePart{
				{Type: openai.ChatMessagePartTypeText, Text: visionUserPrompt},
				{
					Type: openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{
						URL:    dataURL,
						Detail: openai.ImageURLDetailAuto,
					},
				},
			},
		},
	}
}

// completeContact sends the given messages to the LLM and parses the
// response as a Contact.
func (a *Agent) completeContact(ctx context.Context, messages []openai.ChatCompletionMessage) (*model.Contact, error) {
	resp, err := a.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    a.model,
		Messages: messages,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no completion choices returned")
	}

	raw := extractJSON(resp.Choices[0].Message.Content)

	var contact model.Contact
	if err := json.Unmarshal([]byte(raw), &contact); err != nil {
		return nil, fmt.Errorf("unmarshal LLM response: %w", err)
	}

	return &contact, nil
}

// encodeImageDataURL reads the image file at path and returns it encoded as
// a base64 data: URL suitable for a vision model's image_url content part.
func encodeImageDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("image file is empty")
	}

	mimeType := http.DetectContentType(data)
	if !strings.HasPrefix(mimeType, "image/") {
		return "", fmt.Errorf("file does not appear to be an image (detected %s)", mimeType)
	}

	encoded := base64.StdEncoding.EncodeToString(data)

	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

// extractJSON strips optional markdown code fences some models add despite
// being asked for raw JSON.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// GenerateMarkdown renders a Contact as the required Obsidian Person note.
func GenerateMarkdown(c *model.Contact) string {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("name: %s\n", c.Name))
	b.WriteString("aliases:\n")
	b.WriteString(fmt.Sprintf("birthday: %s\n", c.Birthday))
	b.WriteString(fmt.Sprintf("phone: %s\n", c.Phone))
	b.WriteString(fmt.Sprintf("email: %s\n", c.Email))
	b.WriteString(fmt.Sprintf("telegram: %s\n", c.Telegram))
	b.WriteString(fmt.Sprintf("linkedin: %s\n", c.LinkedIn))
	b.WriteString(fmt.Sprintf("company: %s\n", c.Company))
	b.WriteString(fmt.Sprintf("position: %s\n", c.Position))
	b.WriteString(fmt.Sprintf("where worked: %s\n", c.WhereWorked))
	b.WriteString(fmt.Sprintf("relationship: %s\n", c.Relationship))
	b.WriteString(fmt.Sprintf("category: %s\n", c.Category))
	b.WriteString(fmt.Sprintf("projects: %s\n", c.Projects))
	b.WriteString("---\n\n")
	b.WriteString("tags:: [[👥 People MOC]]\n\n")
	b.WriteString("## Auto-Generated Information\n\n")
	b.WriteString("### Last Contact\n")
	b.WriteString("```dataview\n")
	b.WriteString("TABLE file.mtime as \"Date\", file.link as \"Note\"\n")
	b.WriteString("FROM \"/\"\n")
	b.WriteString("WHERE contains(file.outlinks, this.file.link) OR contains(file.name, this.name)\n")
	b.WriteString("SORT file.mtime DESC\n")
	b.WriteString("LIMIT 1\n")
	b.WriteString("```\n")

	return b.String()
}

// SanitizeFilename replaces characters that are unsafe in filenames and
// trims surrounding whitespace.
func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = invalidFilenameChars.Replace(name)
	return strings.TrimSpace(name)
}

// FilePath returns the vault path a Contact with the given name should be
// written to.
func FilePath(name string) string {
	return fmt.Sprintf("Contacts/%s.md", SanitizeFilename(name))
}
