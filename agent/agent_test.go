package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"obsidian-agent/model"
)

// mockWriter records the last Write call and returns a configurable error.
type mockWriter struct {
	path    string
	content string
	calls   int
	err     error
}

func (m *mockWriter) Write(_ context.Context, path, content string) error {
	m.calls++
	m.path = path
	m.content = content
	return m.err
}

var _ ContactWriter = (*mockWriter)(nil)

func TestGenerateMarkdown(t *testing.T) {
	c := &model.Contact{
		Name:         "John Smith",
		Birthday:     "1985-04-12",
		Phone:        "+998901234567",
		Email:        "john@gmail.com",
		Telegram:     "@johnsmith",
		LinkedIn:     "https://linkedin.com/in/johnsmith",
		Company:      "Google",
		Position:     "Software Engineer",
		WhereWorked:  "Google",
		Relationship: "Friend",
		Category:     "Client",
		Projects:     "Project Atlas",
	}

	got := GenerateMarkdown(c)
	want := "---\n" +
		"name: John Smith\n" +
		"aliases:\n" +
		"birthday: 1985-04-12\n" +
		"phone: +998901234567\n" +
		"email: john@gmail.com\n" +
		"telegram: @johnsmith\n" +
		"linkedin: https://linkedin.com/in/johnsmith\n" +
		"company: Google\n" +
		"position: Software Engineer\n" +
		"where worked: Google\n" +
		"relationship: Friend\n" +
		"category: Client\n" +
		"projects: Project Atlas\n" +
		"---\n\n" +
		"tags:: [[👥 People MOC]]\n\n" +
		"## Auto-Generated Information\n\n" +
		"### Last Contact\n" +
		"```dataview\n" +
		"TABLE file.mtime as \"Date\", file.link as \"Note\"\n" +
		"FROM \"/\"\n" +
		"WHERE contains(file.outlinks, this.file.link) OR contains(file.name, this.name)\n" +
		"SORT file.mtime DESC\n" +
		"LIMIT 1\n" +
		"```\n"

	if got != want {
		t.Errorf("GenerateMarkdown() =\n%q\nwant\n%q", got, want)
	}
}

func TestGenerateMarkdown_EmptyFieldsArePreserved(t *testing.T) {
	c := &model.Contact{
		Name:    "Jane Doe",
		Company: "Acme",
	}

	got := GenerateMarkdown(c)
	want := "---\n" +
		"name: Jane Doe\n" +
		"aliases:\n" +
		"birthday: \n" +
		"phone: \n" +
		"email: \n" +
		"telegram: \n" +
		"linkedin: \n" +
		"company: Acme\n" +
		"position: \n" +
		"where worked: \n" +
		"relationship: \n" +
		"category: \n" +
		"projects: \n" +
		"---\n\n" +
		"tags:: [[👥 People MOC]]\n\n" +
		"## Auto-Generated Information\n\n" +
		"### Last Contact\n" +
		"```dataview\n" +
		"TABLE file.mtime as \"Date\", file.link as \"Note\"\n" +
		"FROM \"/\"\n" +
		"WHERE contains(file.outlinks, this.file.link) OR contains(file.name, this.name)\n" +
		"SORT file.mtime DESC\n" +
		"LIMIT 1\n" +
		"```\n"

	if got != want {
		t.Errorf("GenerateMarkdown() =\n%q\nwant\n%q", got, want)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "John Smith", "John Smith"},
		{"slash", "John/Smith", "John-Smith"},
		{"backslash", `John\Smith`, "John-Smith"},
		{"colon", "John: Smith", "John- Smith"},
		{"question mark", "John? Smith", "John- Smith"},
		{"quotes", `"John Smith"`, "-John Smith-"},
		{"angle brackets", "<John>", "-John-"},
		{"pipe", "John|Smith", "John-Smith"},
		{"wildcard", "John*Smith", "John-Smith"},
		{"trims whitespace", "  John Smith  ", "John Smith"},
		{"all unsafe", `/\:*?"<>|`, "---------"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFilename(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFilePath(t *testing.T) {
	got := FilePath("John Smith")
	want := "Contacts/John Smith.md"

	if got != want {
		t.Errorf("FilePath() = %q, want %q", got, want)
	}

	got = FilePath("John/Smith")
	want = "Contacts/John-Smith.md"

	if got != want {
		t.Errorf("FilePath() = %q, want %q", got, want)
	}
}

func TestAgent_WriteContact_UsesMockWriter(t *testing.T) {
	writer := &mockWriter{}
	a := New(nil, "test-model", writer)

	c := &model.Contact{
		Name:    "John Smith",
		Company: "Google",
	}

	if err := a.writeContact(context.Background(), c); err != nil {
		t.Fatalf("writeContact() error = %v", err)
	}

	if writer.calls != 1 {
		t.Fatalf("expected 1 call to Write, got %d", writer.calls)
	}
	if writer.path != "Contacts/John Smith.md" {
		t.Errorf("writer.path = %q, want %q", writer.path, "Contacts/John Smith.md")
	}
	if writer.content != GenerateMarkdown(c) {
		t.Errorf("writer.content = %q, want %q", writer.content, GenerateMarkdown(c))
	}
}

func TestAgent_WriteContact_PropagatesWriterError(t *testing.T) {
	writer := &mockWriter{err: errors.New("write failed")}
	a := New(nil, "test-model", writer)

	c := &model.Contact{Name: "John Smith"}

	err := a.writeContact(context.Background(), c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAgent_CreateContact_EmptyInput(t *testing.T) {
	writer := &mockWriter{}
	a := New(nil, "test-model", writer)

	_, err := a.CreateContact(context.Background(), "   ")
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if writer.calls != 0 {
		t.Errorf("expected no Write calls for empty input, got %d", writer.calls)
	}
}

// pngSignature is the minimal set of bytes http.DetectContentType needs to
// sniff a file as image/png.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestEncodeImageDataURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "card.png")
	if err := os.WriteFile(path, pngSignature, 0o600); err != nil {
		t.Fatalf("failed to write test image: %v", err)
	}

	dataURL, err := encodeImageDataURL(path)
	if err != nil {
		t.Fatalf("encodeImageDataURL() error = %v", err)
	}

	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Errorf("dataURL = %q, want prefix %q", dataURL, "data:image/png;base64,")
	}
}

func TestEncodeImageDataURL_RejectsNonImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("just some plain text, not an image"), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := encodeImageDataURL(path)
	if err == nil {
		t.Fatal("expected error for non-image file, got nil")
	}
}

func TestEncodeImageDataURL_MissingFile(t *testing.T) {
	_, err := encodeImageDataURL("/nonexistent/path/card.png")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestBuildImageMessages(t *testing.T) {
	dataURL := "data:image/png;base64,AAAA"
	messages := buildImageMessages(dataURL)

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != openai.ChatMessageRoleSystem {
		t.Errorf("messages[0].Role = %q, want %q", messages[0].Role, openai.ChatMessageRoleSystem)
	}
	if messages[1].Role != openai.ChatMessageRoleUser {
		t.Errorf("messages[1].Role = %q, want %q", messages[1].Role, openai.ChatMessageRoleUser)
	}

	parts := messages[1].MultiContent
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(parts))
	}
	if parts[0].Type != openai.ChatMessagePartTypeText {
		t.Errorf("parts[0].Type = %q, want %q", parts[0].Type, openai.ChatMessagePartTypeText)
	}
	if parts[1].Type != openai.ChatMessagePartTypeImageURL {
		t.Errorf("parts[1].Type = %q, want %q", parts[1].Type, openai.ChatMessagePartTypeImageURL)
	}
	if parts[1].ImageURL == nil || parts[1].ImageURL.URL != dataURL {
		t.Errorf("parts[1].ImageURL = %+v, want URL %q", parts[1].ImageURL, dataURL)
	}
}

// newTestAgent returns an Agent whose LLM client talks to a fake
// OpenAI-compatible server that always replies with the given content.
func newTestAgent(t *testing.T, content string, writer ContactWriter) *Agent {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := openai.DefaultConfig("test-key")
	cfg.BaseURL = srv.URL + "/v1"

	return New(openai.NewClientWithConfig(cfg), "test-model", writer)
}

// writeTestImage writes a minimal PNG to a temp dir and returns its path.
func writeTestImage(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(path, pngSignature, 0o600); err != nil {
		t.Fatalf("failed to write test image: %v", err)
	}

	return path
}

const testContactJSON = `{"name": "John Smith", "company": "Google"}`

func TestAgent_ExtractContact_DoesNotWrite(t *testing.T) {
	writer := &mockWriter{}
	a := newTestAgent(t, testContactJSON, writer)

	c, err := a.ExtractContact(context.Background(), "  John Smith from Google  ")
	if err != nil {
		t.Fatalf("ExtractContact() error = %v", err)
	}
	if c.Name != "John Smith" || c.Company != "Google" {
		t.Errorf("ExtractContact() = %+v, want Name=John Smith Company=Google", c)
	}
	if writer.calls != 0 {
		t.Errorf("expected no Write calls, got %d", writer.calls)
	}
}

func TestAgent_ExtractContact_EmptyInput(t *testing.T) {
	writer := &mockWriter{}
	a := New(nil, "test-model", writer)

	if _, err := a.ExtractContact(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if writer.calls != 0 {
		t.Errorf("expected no Write calls, got %d", writer.calls)
	}
}

func TestAgent_ExtractContact_MissingName(t *testing.T) {
	writer := &mockWriter{}
	a := newTestAgent(t, `{"name": "  ", "company": "Google"}`, writer)

	if _, err := a.ExtractContact(context.Background(), "someone at Google"); err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
	if writer.calls != 0 {
		t.Errorf("expected no Write calls, got %d", writer.calls)
	}
}

func TestAgent_ExtractContactFromImage_DoesNotWrite(t *testing.T) {
	writer := &mockWriter{}
	a := newTestAgent(t, testContactJSON, writer)

	c, err := a.ExtractContactFromImage(context.Background(), writeTestImage(t))
	if err != nil {
		t.Fatalf("ExtractContactFromImage() error = %v", err)
	}
	if c.Name != "John Smith" {
		t.Errorf("c.Name = %q, want %q", c.Name, "John Smith")
	}
	if writer.calls != 0 {
		t.Errorf("expected no Write calls, got %d", writer.calls)
	}
}

func TestAgent_ExtractContactFromImage_MissingName(t *testing.T) {
	writer := &mockWriter{}
	a := newTestAgent(t, `{"name": ""}`, writer)

	if _, err := a.ExtractContactFromImage(context.Background(), writeTestImage(t)); err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
	if writer.calls != 0 {
		t.Errorf("expected no Write calls, got %d", writer.calls)
	}
}

func TestAgent_CreateContact_Writes(t *testing.T) {
	writer := &mockWriter{}
	a := newTestAgent(t, testContactJSON, writer)

	c, err := a.CreateContact(context.Background(), "John Smith from Google")
	if err != nil {
		t.Fatalf("CreateContact() error = %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("expected 1 Write call, got %d", writer.calls)
	}
	if writer.path != "Contacts/John Smith.md" {
		t.Errorf("writer.path = %q, want %q", writer.path, "Contacts/John Smith.md")
	}
	if writer.content != GenerateMarkdown(c) {
		t.Errorf("writer.content = %q, want %q", writer.content, GenerateMarkdown(c))
	}
}

func TestAgent_CreateContact_PropagatesWriterError(t *testing.T) {
	writer := &mockWriter{err: errors.New("write failed")}
	a := newTestAgent(t, testContactJSON, writer)

	_, err := a.CreateContact(context.Background(), "John Smith from Google")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, writer.err) {
		t.Errorf("error = %v, want it to wrap %v", err, writer.err)
	}
}

func TestAgent_CreateContactFromImage_Writes(t *testing.T) {
	writer := &mockWriter{}
	a := newTestAgent(t, testContactJSON, writer)

	c, err := a.CreateContactFromImage(context.Background(), writeTestImage(t))
	if err != nil {
		t.Fatalf("CreateContactFromImage() error = %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("expected 1 Write call, got %d", writer.calls)
	}
	if writer.path != "Contacts/John Smith.md" {
		t.Errorf("writer.path = %q, want %q", writer.path, "Contacts/John Smith.md")
	}
	if writer.content != GenerateMarkdown(c) {
		t.Errorf("writer.content = %q, want %q", writer.content, GenerateMarkdown(c))
	}
}
