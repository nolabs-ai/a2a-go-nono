// Package config holds settings shared by the worker and orchestrator agents.
package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"
)

// DefaultModel matches the Python demo's default Gemini model.
const DefaultModel = "gemini-2.5-flash"

// APIKeyEnvs lists the variables nono may fill with a phantom Gemini
// credential. Older releases used GOOGLE_API_KEY; current ones also expose the
// names other Google SDKs expect.
var APIKeyEnvs = []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"}

// APIKey returns the first credential variable that is set.
func APIKey() string {
	for _, name := range APIKeyEnvs {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// Model returns AGENT_MODEL or the default.
func Model() string {
	if m := strings.TrimSpace(os.Getenv("AGENT_MODEL")); m != "" {
		return m
	}
	return DefaultModel
}

// Getenv returns an environment variable or a fallback.
func Getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// NewModel builds the Gemini model. Under nono the API key is a phantom value
// that the supervised proxy swaps for the real key on approved requests.
func NewModel(ctx context.Context) (model.LLM, error) {
	key := APIKey()
	if key == "" {
		return nil, fmt.Errorf("no Gemini API key found in %s; launch nono with `--credential gemini` as described in the README", strings.Join(APIKeyEnvs, ", "))
	}
	return gemini.NewModel(ctx, Model(), &genai.ClientConfig{APIKey: key, HTTPClient: HTTPClient(0)})
}

// GenerateContentConfig disables Gemini's adjustable content filters so the
// model will request risky-looking operations and nono can demonstrate the
// enforcement boundary. Google's non-adjustable protections still apply.
func GenerateContentConfig() *genai.GenerateContentConfig {
	categories := []genai.HarmCategory{
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
		genai.HarmCategoryDangerousContent,
	}
	cfg := &genai.GenerateContentConfig{}
	for _, c := range categories {
		cfg.SafetySettings = append(cfg.SafetySettings, &genai.SafetySetting{
			Category:  c,
			Threshold: genai.HarmBlockThresholdOff,
		})
	}
	return cfg
}
