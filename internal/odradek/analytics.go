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

	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
	"google.golang.org/genai"
)

type ExtractedMetrics struct {
	Impressions int    `json:"impressions"`
	Reactions   int    `json:"reactions"`
	Comments    int    `json:"comments"`
	Reposts     int    `json:"reposts"`
	PostHint    string `json:"post_hint"`
	Notes       string `json:"notes"`
}

// AnalyticsExtractor reads LinkedIn Analytics screenshots via Gemini Vision or `agy`.
type AnalyticsExtractor struct {
	settings *settings.Manager
}

func NewAnalyticsExtractor(mgr *settings.Manager) *AnalyticsExtractor {
	return &AnalyticsExtractor{settings: mgr}
}

func (a *AnalyticsExtractor) ExtractFromImage(ctx context.Context, imgBytes []byte) (*ExtractedMetrics, error) {
	provider, _ := a.settings.GetString(ctx, settings.KeyQAProvider)
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey, _ := a.settings.GetString(ctx, settings.KeyGeminiAPIKey)
	apiKey = strings.TrimSpace(apiKey)

	if provider == "agy" || (provider != "gemini" && apiKey == "") {
		return a.extractWithAgy(ctx, imgBytes)
	}
	return a.extractWithGemini(ctx, apiKey, imgBytes)
}

func (a *AnalyticsExtractor) extractWithGemini(ctx context.Context, apiKey string, imgBytes []byte) (*ExtractedMetrics, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("setting %s is not configured", settings.KeyGeminiAPIKey)
	}
	model, err := a.settings.GetString(ctx, settings.KeyGeminiModel)
	if err != nil || strings.TrimSpace(model) == "" {
		model = "gemini-2.5-flash"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, err
	}

	parts := []*genai.Part{
		genai.NewPartFromBytes(imgBytes, "image/jpeg"),
		genai.NewPartFromText(metricsPrompt()),
	}
	resp, err := client.Models.GenerateContent(ctx, strings.TrimSpace(model), []*genai.Content{
		genai.NewContentFromParts(parts, genai.RoleUser),
	}, &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	})
	if err != nil {
		return nil, fmt.Errorf("gemini metrics extract: %w", err)
	}
	return parseExtractedMetrics(resp.Text())
}

func (a *AnalyticsExtractor) extractWithAgy(ctx context.Context, imgBytes []byte) (*ExtractedMetrics, error) {
	tmpDir, err := os.MkdirTemp("", "porter-metrics-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	imgPath := filepath.Join(tmpDir, "analytics.jpg")
	if err := os.WriteFile(imgPath, imgBytes, 0o600); err != nil {
		return nil, err
	}

	agyBin, _ := a.settings.GetString(ctx, settings.KeyAgyBin)
	agyBin = resolveAgyBin(agyBin)

	prompt := metricsPrompt() + fmt.Sprintf("\n\nScreenshot path: %s\nUse read_file on that image first.", imgPath)
	cmd := exec.CommandContext(ctx, agyBin,
		"-p", prompt,
		"--output-format", "json",
		"--disable-slash-commands",
		"--mode", "auto",
		"--dangerously-skip-permissions",
		"--print-timeout", "90s",
		"--add-dir", tmpDir,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("agy metrics extract failed: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}

	var env agyPrintEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		return nil, fmt.Errorf("decode agy envelope: %w", err)
	}
	if env.Status != "SUCCESS" {
		return nil, fmt.Errorf("agy returned status %q: %s", env.Status, env.Response)
	}
	return parseExtractedMetrics(env.Response)
}

func metricsPrompt() string {
	return `Inspect this LinkedIn Analytics / Post Impressions screenshot.
Extract the numeric engagement metrics as ONLY valid JSON with keys:
- "impressions": integer (0 if not visible)
- "reactions": integer (likes/reactions, 0 if not visible)
- "comments": integer (0 if not visible)
- "reposts": integer (0 if not visible)
- "post_hint": short snippet of the post text/title if visible in the screenshot
- "notes": brief 1-sentence observation about the reach/engagement shown`
}

func parseExtractedMetrics(raw string) (*ExtractedMetrics, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	dec := json.NewDecoder(strings.NewReader(clean))
	var out ExtractedMetrics
	var decoded bool
	for {
		var candidate ExtractedMetrics
		if err := dec.Decode(&candidate); err != nil {
			if decoded {
				break
			}
			return nil, fmt.Errorf("decode ExtractedMetrics (%q): %w", clean, err)
		}
		out = candidate
		decoded = true
	}
	return &out, nil
}

// FormatInsightsReport builds an actionable Telegram report from SQLite QA + growth metrics.
func FormatInsightsReport(s *store.AnalyticsSummary) string {
	var sb strings.Builder
	sb.WriteString("📊 Porter & Odradek Insights\n\n")
	sb.WriteString(fmt.Sprintf("• Published posts: %d\n", s.TotalPublished))
	if s.TotalQALogs > 0 {
		rate := float64(s.FirstTryPasses) * 100.0 / float64(s.TotalQALogs)
		sb.WriteString(fmt.Sprintf("• QA 1st-try pass rate: %.0f%% (%d/%d)\n", rate, s.FirstTryPasses, s.TotalQALogs))
	}

	if len(s.ByPattern) > 0 {
		sb.WriteString("\n🧭 Performance by Topic Pattern:\n")
		for _, p := range s.ByPattern {
			sb.WriteString(fmt.Sprintf("• %s (%d posts): %.0f impr | %.1f react | %.1f comm\n",
				p.PatternSlug, p.PostCount, p.AvgImpressions, p.AvgReactions, p.AvgComments))
		}
	}

	if len(s.ByMediaType) > 0 {
		sb.WriteString("\n🎨 Performance by Media Type:\n")
		for _, m := range s.ByMediaType {
			sb.WriteString(fmt.Sprintf("• %s (%d posts): %.0f impr | %.1f react\n",
				m.MediaType, m.PostCount, m.AvgImpressions, m.AvgReactions))
		}
	}

	if s.TotalPublished == 0 {
		sb.WriteString("\nNo published posts yet. Publish a few posts and send LinkedIn Analytics screenshots here to track growth.")
	}
	return sb.String()
}
