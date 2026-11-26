package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"codeberg.org/matheus/porter/internal/odradek/curator"
	"codeberg.org/matheus/porter/internal/settings"
	openai "github.com/sashabaranov/go-openai"
)

type WriterInput struct {
	Topic             curator.CuratedTopic
	PatternGuidelines string
	PreviousFeedback  string
	Attempt           int
}

type WriterAgent struct {
	settings *settings.Manager
	baseURL  string
}

func NewWriterAgent(mgr *settings.Manager) *WriterAgent {
	return &WriterAgent{
		settings: mgr,
		baseURL:  "https://api.deepseek.com/v1",
	}
}

func (w *WriterAgent) WithBaseURL(baseURL string) *WriterAgent {
	w.baseURL = strings.TrimRight(baseURL, "/")
	return w
}

func (w *WriterAgent) newClient(ctx context.Context) (*openai.Client, string, error) {
	apiKey, err := w.settings.GetString(ctx, settings.KeyDeepSeekAPIKey)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return nil, "", fmt.Errorf("setting %s is not configured", settings.KeyDeepSeekAPIKey)
	}
	model, err := w.settings.GetString(ctx, settings.KeyDeepSeekModel)
	if err != nil || strings.TrimSpace(model) == "" {
		model = "deepseek-chat"
	}

	cfg := openai.DefaultConfig(strings.TrimSpace(apiKey))
	cfg.BaseURL = w.baseURL
	return openai.NewClientWithConfig(cfg), strings.TrimSpace(model), nil
}

func (w *WriterAgent) DraftPost(ctx context.Context, in WriterInput) (*PostDraft, error) {
	client, model, err := w.newClient(ctx)
	if err != nil {
		return nil, err
	}

	schemaBytes, err := PostDraftSchemaJSON()
	if err != nil {
		return nil, fmt.Errorf("build PostDraft schema: %w", err)
	}

	tone, _ := w.settings.GetString(ctx, settings.KeyWriterTone)
	toneInstruction := toneGuidance(tone)

	systemPrompt := `You are a Senior Software Engineer writing technical LinkedIn posts in direct, natural English (en-US).
Rules:
- Communication tone: ` + toneInstruction + `
- NO AI cliches ("In today's fast-paced world", "Game-changer", "Delve into", "Revolutionize", "Let's unpack").
- Write sharp, concrete engineering insights with short paragraphs and clean line breaks for LinkedIn readability.
- Avoid emoji spam (at most 1-2 functional emojis if truly helpful, or zero).
- Classify the post into a reusable kebab-case topic_pattern_slug and human-readable topic_pattern_label.
- Media selection:
  - Choose "code_snippet" when a short code block (< 25 lines, <= 75 chars/line) clarifies the point.
  - Choose "mermaid" when an architecture/sequence diagram (flowchart TD/LR or sequenceDiagram) clarifies the flow. Keep node labels concise and quoted if containing parentheses.
  - Choose "none" if text alone is strongest.
- If the source material is ambiguous, lacks key facts, or makes unverified benchmark claims, set has_uncertainty=true and explain the exact doubt in uncertainty_reason.
- Return ONLY valid JSON matching this schema:
` + string(schemaBytes)

	var userPrompt strings.Builder
	userPrompt.WriteString(fmt.Sprintf("Source type: %s\nTitle: %s\n", in.Topic.SourceType, in.Topic.Title))
	if in.Topic.SourceURL != "" {
		userPrompt.WriteString(fmt.Sprintf("Reference URL: %s\n", in.Topic.SourceURL))
	}
	if in.Topic.Angle != "" {
		userPrompt.WriteString(fmt.Sprintf("Requested angle: %s\n", in.Topic.Angle))
	}
	if in.PatternGuidelines != "" {
		userPrompt.WriteString(fmt.Sprintf("User-defined guidelines for this topic pattern:\n%s\n", in.PatternGuidelines))
	}
	userPrompt.WriteString(fmt.Sprintf("\nContext / Source content:\n%s\n", in.Topic.Summary))

	if in.PreviousFeedback != "" {
		userPrompt.WriteString(fmt.Sprintf("\nCRITICAL — Attempt #%d QA Feedback to fix:\n%s\n", in.Attempt, in.PreviousFeedback))
	}

	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt.String()},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
		Temperature: 0.4,
	})
	if err != nil {
		return nil, fmt.Errorf("deepseek DraftPost: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("deepseek returned zero choices")
	}

	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	var draft PostDraft
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		return nil, fmt.Errorf("decode PostDraft JSON: %w (raw=%q)", err, raw)
	}
	if err := ValidatePostDraft(&draft); err != nil {
		return nil, err
	}
	return &draft, nil
}

func (w *WriterAgent) DraftSidequestArticle(ctx context.Context, in WriterInput, draft *PostDraft) (*SidequestArticle, error) {
	client, model, err := w.newClient(ctx)
	if err != nil {
		return nil, err
	}

	schemaBytes, err := SidequestArticleSchemaJSON()
	if err != nil {
		return nil, fmt.Errorf("build SidequestArticle schema: %w", err)
	}

	systemPrompt := `You are a Senior Software Engineer writing a deep-dive technical blog article in English for sidequestlab.dev (Astro MDX).
Return ONLY valid JSON matching this schema:
` + string(schemaBytes)

	userPrompt := fmt.Sprintf(
		"Expand this topic and LinkedIn post into a thorough, practical engineering article in Markdown (no frontmatter in markdown_body).\n\nTitle: %s\nAngle: %s\nLinkedIn Post:\n%s\n\nSource Context:\n%s",
		in.Topic.Title, in.Topic.Angle, draft.FormatForLinkedIn(), in.Topic.Summary,
	)

	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
		Temperature: 0.4,
	})
	if err != nil {
		return nil, fmt.Errorf("deepseek DraftSidequestArticle: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("deepseek returned zero choices")
	}

	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	var article SidequestArticle
	if err := json.Unmarshal([]byte(raw), &article); err != nil {
		return nil, fmt.Errorf("decode SidequestArticle JSON: %w", err)
	}
	if err := ValidateSidequestArticle(&article); err != nil {
		return nil, err
	}
	return &article, nil
}

func toneGuidance(tone string) string {
	switch strings.ToLower(strings.TrimSpace(tone)) {
	case "informational":
		return "INFORMATIONAL & EDUCATIONAL — structured, clear explanations, practical takeaways, and helpful context for developers learning the topic."
	case "informal":
		return "INFORMAL & CONVERSATIONAL — relaxed builder-in-public voice, first-person storytelling, approachable and authentic, still grounded in real engineering details."
	case "formal":
		return "FORMAL & ARCHITECTURAL — polished, executive engineering memo style, precise terminology, objective and authoritative."
	default:
		return "TECHNICAL & DIRECT — senior-to-senior engineering voice, concise, focused on architecture, implementation details, and real tradeoffs."
	}
}
