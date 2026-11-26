package curator

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
	"google.golang.org/genai"
)

type VoiceIntent struct {
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	Angle           string `json:"angle"`
	TargetSidequest bool   `json:"target_sidequest"`
}

type AudioTranscriber struct {
	settings *settings.Manager
	model    string
}

func NewAudioTranscriber(mgr *settings.Manager) *AudioTranscriber {
	return &AudioTranscriber{
		settings: mgr,
		model:    "gemini-2.5-flash",
	}
}

// TranscribeOGG sends Telegram voice note bytes to Gemini Flash or local `agy` CLI.
func (a *AudioTranscriber) TranscribeOGG(ctx context.Context, oggBytes []byte) (*VoiceIntent, error) {
	provider, _ := a.settings.GetString(ctx, settings.KeyQAProvider)
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey, _ := a.settings.GetString(ctx, settings.KeyGeminiAPIKey)
	apiKey = strings.TrimSpace(apiKey)

	if provider == "agy" || (provider != "gemini" && apiKey == "") {
		return a.transcribeWithAgy(ctx, oggBytes)
	}
	return a.transcribeWithGemini(ctx, apiKey, oggBytes)
}

func (a *AudioTranscriber) transcribeWithGemini(ctx context.Context, apiKey string, oggBytes []byte) (*VoiceIntent, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("setting %s is not configured", settings.KeyGeminiAPIKey)
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("init gemini client: %w", err)
	}

	parts := []*genai.Part{
		genai.NewPartFromBytes(oggBytes, "audio/ogg"),
		genai.NewPartFromText(voicePrompt()),
	}

	resp, err := client.Models.GenerateContent(ctx, a.model, []*genai.Content{
		genai.NewContentFromParts(parts, genai.RoleUser),
	}, &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	})
	if err != nil {
		return nil, fmt.Errorf("gemini audio transcription: %w", err)
	}

	return parseVoiceIntent(resp.Text())
}

func (a *AudioTranscriber) transcribeWithAgy(ctx context.Context, oggBytes []byte) (*VoiceIntent, error) {
	tmpDir, err := os.MkdirTemp("", "porter-voice-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	oggPath := filepath.Join(tmpDir, "voice.ogg")
	if err := os.WriteFile(oggPath, oggBytes, 0o600); err != nil {
		return nil, err
	}

	agyBin, _ := a.settings.GetString(ctx, settings.KeyAgyBin)
	agyBin = resolveAgyBin(agyBin)

	prompt := voicePrompt() + fmt.Sprintf("\n\nAudio file path: %s\nUse read_file on that audio file first.", oggPath)
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
		return nil, fmt.Errorf("agy audio transcription failed: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}

	var env struct {
		Status   string `json:"status"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		return nil, fmt.Errorf("decode agy audio envelope: %w", err)
	}
	if env.Status != "SUCCESS" {
		return nil, fmt.Errorf("agy audio status %q: %s", env.Status, env.Response)
	}
	return parseVoiceIntent(env.Response)
}

func voicePrompt() string {
	return `Listen to this voice note (it may be in Portuguese or English).
Extract the user's technical post idea as ONLY valid JSON with keys:
- "title": concise English title
- "summary": faithful English summary of what the user explained
- "angle": specific angle or emphasis requested
- "target_sidequest": boolean, true only if the user explicitly asked to also publish on sidequestlab.dev / blog.`
}

func parseVoiceIntent(raw string) (*VoiceIntent, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	dec := json.NewDecoder(strings.NewReader(clean))
	var intent VoiceIntent
	var decoded bool
	for {
		var candidate VoiceIntent
		if err := dec.Decode(&candidate); err != nil {
			if decoded {
				break
			}
			return nil, fmt.Errorf("decode voice intent JSON (%q): %w", clean, err)
		}
		intent = candidate
		decoded = true
	}
	if strings.TrimSpace(intent.Title) == "" {
		intent.Title = "Voice note technical idea"
	}
	return &intent, nil
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
