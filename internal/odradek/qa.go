package odradek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/settings"
	"google.golang.org/genai"
)

// QAAgent is Odradek's multimodal reviewer using Google Gemini Flash or local `agy` CLI.
type QAAgent struct {
	settings *settings.Manager
}

func NewQAAgent(mgr *settings.Manager) *QAAgent {
	return &QAAgent{settings: mgr}
}

func (q *QAAgent) Review(ctx context.Context, topicSummary string, draft *agents.PostDraft, pngPath string) (*agents.QAReview, error) {
	provider, _ := q.settings.GetString(ctx, settings.KeyQAProvider)
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey, _ := q.settings.GetString(ctx, settings.KeyGeminiAPIKey)
	apiKey = strings.TrimSpace(apiKey)

	if provider == "agy" || (provider != "gemini" && apiKey == "") {
		return q.reviewWithAgy(ctx, topicSummary, draft, pngPath)
	}
	return q.reviewWithGemini(ctx, apiKey, topicSummary, draft, pngPath)
}

func (q *QAAgent) reviewWithGemini(ctx context.Context, apiKey, topicSummary string, draft *agents.PostDraft, pngPath string) (*agents.QAReview, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("setting %s is not configured", settings.KeyGeminiAPIKey)
	}
	model, err := q.settings.GetString(ctx, settings.KeyGeminiModel)
	if err != nil || strings.TrimSpace(model) == "" {
		model = "gemini-2.5-flash"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("init gemini qa client: %w", err)
	}

	prompt, err := buildQAPrompt(topicSummary, draft, pngPath != "")
	if err != nil {
		return nil, err
	}

	parts := []*genai.Part{genai.NewPartFromText(prompt)}
	if pngPath != "" {
		imgBytes, err := os.ReadFile(pngPath)
		if err != nil {
			return nil, fmt.Errorf("read rendered png %s: %w", pngPath, err)
		}
		parts = append(parts, genai.NewPartFromBytes(imgBytes, "image/png"))
	}

	resp, err := client.Models.GenerateContent(ctx, strings.TrimSpace(model), []*genai.Content{
		genai.NewContentFromParts(parts, genai.RoleUser),
	}, &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	})
	if err != nil {
		return nil, fmt.Errorf("gemini QA review: %w", err)
	}

	return parseQAReview(resp.Text())
}

type agyPrintEnvelope struct {
	Status   string `json:"status"`
	Response string `json:"response"`
}

func (q *QAAgent) reviewWithAgy(ctx context.Context, topicSummary string, draft *agents.PostDraft, pngPath string) (*agents.QAReview, error) {
	agyBin, _ := q.settings.GetString(ctx, settings.KeyAgyBin)
	agyBin = resolveAgyBin(agyBin)

	prompt, err := buildQAPrompt(topicSummary, draft, pngPath != "")
	if err != nil {
		return nil, err
	}
	if pngPath != "" {
		prompt += fmt.Sprintf("\n\nRendered PNG image path: %s\nUse read_file on that PNG path to visually inspect the rendered card before responding.", pngPath)
	}

	schemaBytes, err := agents.QAReviewSchemaJSON()
	if err != nil {
		return nil, err
	}

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--json-schema", string(schemaBytes),
		"--disable-slash-commands",
		"--mode", "auto",
		"--dangerously-skip-permissions",
		"--print-timeout", "90s",
	}
	if pngPath != "" {
		args = append(args, "--add-dir", filepath.Dir(pngPath))
	}

	cmd := exec.CommandContext(ctx, agyBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("agy QA failed: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}

	var env agyPrintEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		return nil, fmt.Errorf("decode agy envelope: %w (raw=%q)", err, stdout.String())
	}
	if env.Status != "SUCCESS" {
		return nil, fmt.Errorf("agy QA returned status %q: %s", env.Status, env.Response)
	}
	return parseQAReview(env.Response)
}

func resolveAgyBin(configured string) string {
	bin := strings.TrimSpace(configured)
	if bin == "" {
		bin = "agy"
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	fallback := filepath.Join(home, ".local", "bin", "agy")
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	return bin
}

func buildQAPrompt(topicSummary string, draft *agents.PostDraft, hasImage bool) (string, error) {
	schemaBytes, err := agents.QAReviewSchemaJSON()
	if err != nil {
		return "", err
	}
	imgNote := "No image attached."
	if hasImage {
		imgNote = "Inspect the attached/referenced PNG card visually for clipping (visual_clipping), overlapping labels/arrows (visual_overlap), low contrast/readability (visual_readability), and typos (visual_typo)."
	}
	return fmt.Sprintf(`You are Odradek, a strict Senior Engineering QA Reviewer.
Evaluate this LinkedIn post draft (written in English).

Check:
1. English grammar, clarity, and natural engineering tone (category: grammar_en).
2. AI cliches or robotic fluff (category: ai_cliche).
3. Technical accuracy against the source context (category: technical_accuracy).
4. Visual inspection: %s
5. Assign confidence_score (1-10) and set has_uncertainty=true (with uncertainty_reason) if any claim is unverified, ambiguous, or requires human sign-off before publishing.

Return ONLY valid JSON matching this schema:
%s

Source Context:
%s

LinkedIn Post Draft:
%s`, imgNote, string(schemaBytes), topicSummary, draft.FormatForLinkedIn()), nil
}

func parseQAReview(raw string) (*agents.QAReview, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	dec := json.NewDecoder(strings.NewReader(clean))
	var review agents.QAReview
	var decoded bool
	for {
		var candidate agents.QAReview
		if err := dec.Decode(&candidate); err != nil {
			if decoded {
				break
			}
			return nil, fmt.Errorf("decode QAReview JSON (%q): %w", clean, err)
		}
		review = candidate
		decoded = true
	}
	if err := agents.ValidateQAReview(&review); err != nil {
		return nil, err
	}
	return &review, nil
}
