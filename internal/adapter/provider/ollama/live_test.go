//go:build ollama_live

package ollama_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bRRRITSCOLD/pythia/internal/adapter/provider/ollama"
	"github.com/bRRRITSCOLD/pythia/internal/core"
)

// TestProvider_Live_Chat_RealOllamaAnswers hits a real, locally running
// Ollama server. It is opt-in via the "ollama_live" build tag
// (go test -tags ollama_live ./...) because it requires `ollama serve` and
// a pulled model on the machine running the test — it is not part of the
// default (CI) test run.
func TestProvider_Live_Chat_RealOllamaAnswers(t *testing.T) {
	baseURL := os.Getenv("OLLAMA_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "qwen3.5"
	}

	p := ollama.New(baseURL, model, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch, err := p.Chat(ctx, core.ChatRequest{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "Reply with exactly one word: pong"},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v (is `ollama serve` running with model %q pulled?)", err, model)
	}

	var text string
	var sawDone bool
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		text += ev.TextDelta
		if ev.Done {
			sawDone = true
		}
	}

	if !sawDone {
		t.Error("stream closed without a terminal Done event")
	}
	if text == "" {
		t.Error("got empty response text from a real Ollama call")
	}
}
