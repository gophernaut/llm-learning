package agents

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openai/openai-go/v3/option"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

const (
	defaultOpenAIModel = "gpt-5.4-mini"
	defaultGeminiModel = "gemini-3.8-flash"
)

// NewModel picks the provider from the environment. LLM_PROVIDER=openai|gemini
// wins; otherwise OpenAI is used when OPENAI_API_KEY is set, Gemini when not.
// name overrides OPENAI_MODEL / GEMINI_MODEL when non-empty.
//
// Both clients retry 429/5xx. One alert costs a few dozen model calls, and a
// failed stage leaves its OutputKey unset, which stops every stage after it.
func NewModel(ctx context.Context, name string) (model.LLM, error) {
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "gemini"
		if os.Getenv("OPENAI_API_KEY") != "" {
			provider = "openai"
		}
	}
	switch provider {
	case "openai":
		if name == "" {
			name = envOr("OPENAI_MODEL", defaultOpenAIModel)
		}
		return openaimodel.NewModel(ctx, name, &openaimodel.ClientConfig{
			APIKey:  os.Getenv("OPENAI_API_KEY"),
			BaseURL: os.Getenv("OPENAI_BASE_URL"),
			Options: []option.RequestOption{option.WithMaxRetries(5)},
		})
	case "gemini":
		if name == "" {
			name = envOr("GEMINI_MODEL", defaultGeminiModel)
		}
		attempts := int32(6)
		initial, maxDelay := 2.0, 30.0
		// The timeout bounds the whole streamed reply, not the first byte.
		timeout := 5 * time.Minute
		return gemini.NewModel(ctx, name, &genai.ClientConfig{
			APIKey: os.Getenv("GOOGLE_API_KEY"),
			HTTPOptions: genai.HTTPOptions{
				Timeout: &timeout,
				RetryOptions: &genai.HTTPRetryOptions{
					Attempts: &attempts, InitialDelay: &initial, MaxDelay: &maxDelay,
				},
			},
		})
	default:
		return nil, fmt.Errorf("unknown LLM_PROVIDER %q (want openai or gemini)", provider)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
