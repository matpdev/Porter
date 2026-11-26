package agents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/invopop/jsonschema"
)

var validate = validator.New()

type MediaType string

const (
	MediaNone        MediaType = "none"
	MediaCodeSnippet MediaType = "code_snippet"
	MediaMermaid     MediaType = "mermaid"
)

type PostMedia struct {
	Type     MediaType `json:"type" validate:"required,oneof=none code_snippet mermaid" jsonschema:"enum=none,enum=code_snippet,enum=mermaid"`
	Title    string    `json:"title,omitempty" validate:"max=60" jsonschema:"description=Short header title displayed on the visual card"`
	Language string    `json:"language,omitempty" validate:"max=30" jsonschema:"description=Syntax highlighting language (e.g. go, typescript, sql) when type=code_snippet"`
	Content  string    `json:"content,omitempty" jsonschema:"description=Clean code (max 25 lines, max 75 chars/line) OR valid Mermaid diagram (flowchart/sequenceDiagram)"`
}

// PostDraft is the structured output returned by the Writer Agent (DeepSeek).
type PostDraft struct {
	TopicPatternSlug  string    `json:"topic_pattern_slug" validate:"required,min=2,max=50" jsonschema:"description=Kebab-case topic category slug (e.g. go-concurrency, sqlite-internals)"`
	TopicPatternLabel string    `json:"topic_pattern_label" validate:"required,min=2,max=60" jsonschema:"description=Human-readable topic category label"`
	Hook              string    `json:"hook" validate:"required,min=10,max=220" jsonschema:"description=First technical impact line in English, no AI cliches"`
	Body              string    `json:"body" validate:"required,min=100,max=2400" jsonschema:"description=Post body in direct technical English with line breaks for LinkedIn"`
	CallToAction      string    `json:"call_to_action" validate:"required,max=250" jsonschema:"description=Open technical question in English to spark engineering discussion"`
	Hashtags          []string  `json:"hashtags" validate:"required,min=2,max=5,dive,startswith=#"`
	Media             PostMedia `json:"media" validate:"required"`
	HasUncertainty    bool      `json:"has_uncertainty" jsonschema:"description=True if the writer is unsure about any factual claim, benchmark, or positioning"`
	UncertaintyReason string    `json:"uncertainty_reason,omitempty" jsonschema:"description=Specific doubt or question for the human reviewer when has_uncertainty=true"`
	WriterNotes       string    `json:"writer_notes,omitempty" jsonschema:"description=Brief notes on choices or fixes applied after QA feedback"`
}

// SidequestArticle is the structured output for publishing bilingual (EN + PT) MDX posts to sidequestlab.dev.
type SidequestArticle struct {
	Slug           string   `json:"slug" validate:"required,min=3,max=80" jsonschema:"description=URL-friendly kebab-case slug shared by EN and PT files"`
	Title          string   `json:"title" validate:"required,min=5,max=120" jsonschema:"description=English article title"`
	Description    string   `json:"description" validate:"required,min=20,max=260" jsonschema:"description=English meta description"`
	TitlePT        string   `json:"title_pt" validate:"required,min=5,max=120" jsonschema:"description=Portuguese (PT-BR) article title"`
	DescriptionPT  string   `json:"description_pt" validate:"required,min=20,max=260" jsonschema:"description=Portuguese (PT-BR) meta description"`
	Type           string   `json:"type" validate:"required,oneof=acerto erro ideia plano life" jsonschema:"enum=acerto,enum=erro,enum=ideia,enum=plano,enum=life"`
	Area           string   `json:"area" validate:"required,oneof=embarcados backend frontend mobile web jogos all" jsonschema:"enum=embarcados,enum=backend,enum=frontend,enum=mobile,enum=web,enum=jogos,enum=all"`
	Tags           []string `json:"tags" validate:"required,min=1,max=8"`
	Stack          []string `json:"stack" validate:"required,min=1,max=8"`
	CoverAlt       string   `json:"cover_alt" validate:"max=180"`
	CoverAltPT     string   `json:"cover_alt_pt" validate:"max=180"`
	MarkdownBody   string   `json:"markdown_body" validate:"required,min=300" jsonschema:"description=Full technical article body in English Markdown (no frontmatter)"`
	MarkdownBodyPT string   `json:"markdown_body_pt" validate:"required,min=300" jsonschema:"description=Full technical article body in Portuguese PT-BR Markdown (no frontmatter)"`
}

type QAIssue struct {
	Category       string `json:"category" validate:"required,oneof=grammar_en ai_cliche technical_accuracy visual_clipping visual_overlap visual_readability visual_typo" jsonschema:"enum=grammar_en,enum=ai_cliche,enum=technical_accuracy,enum=visual_clipping,enum=visual_overlap,enum=visual_readability,enum=visual_typo"`
	Severity       string `json:"severity" validate:"required,oneof=critical minor" jsonschema:"enum=critical,enum=minor"`
	Description    string `json:"description" validate:"required"`
	FixInstruction string `json:"fix_instruction" validate:"required"`
}

// QAReview is the structured output returned by Odradek (Gemini Flash Multimodal).
type QAReview struct {
	Approved          bool      `json:"approved" jsonschema:"description=True only when there are zero critical issues"`
	TextScore         int       `json:"text_score" validate:"required,min=1,max=10"`
	VisualScore       *int      `json:"visual_score" validate:"omitempty,min=1,max=10"`
	ConfidenceScore   int       `json:"confidence_score" validate:"required,min=1,max=10" jsonschema:"description=Odradek confidence (1-10) that this post is factually solid and ready to publish without human review"`
	HasUncertainty    bool      `json:"has_uncertainty" jsonschema:"description=True when claims are ambiguous, unverified, or need human sign-off"`
	UncertaintyReason string    `json:"uncertainty_reason,omitempty" jsonschema:"description=Exact doubt to highlight in Telegram when has_uncertainty=true"`
	Issues            []QAIssue `json:"issues" validate:"required,dive"`
	SummaryFeedback   string    `json:"summary_feedback" validate:"required"`
}

func ValidatePostDraft(d *PostDraft) error {
	seen := make(map[string]struct{}, len(d.Hashtags))
	normalized := make([]string, 0, len(d.Hashtags))
	for _, h := range d.Hashtags {
		cleaned := strings.TrimSpace(strings.TrimLeft(h, "#"))
		cleaned = strings.ReplaceAll(cleaned, " ", "")
		if cleaned == "" {
			continue
		}
		tag := "#" + cleaned
		key := strings.ToLower(tag)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, tag)
		if len(normalized) == 5 {
			break
		}
	}
	d.Hashtags = normalized
	if len(d.Media.Title) > 60 {
		d.Media.Title = strings.TrimSpace(d.Media.Title[:60])
	}
	if err := validate.Struct(d); err != nil {
		return fmt.Errorf("invalid PostDraft: %w", err)
	}
	if d.Media.Type != MediaNone && strings.TrimSpace(d.Media.Content) == "" {
		return fmt.Errorf("invalid PostDraft: media.content is required when media.type=%s", d.Media.Type)
	}
	if d.HasUncertainty && strings.TrimSpace(d.UncertaintyReason) == "" {
		return fmt.Errorf("invalid PostDraft: uncertainty_reason is required when has_uncertainty=true")
	}
	return nil
}

func ValidateSidequestArticle(a *SidequestArticle) error {
	if err := validate.Struct(a); err != nil {
		return fmt.Errorf("invalid SidequestArticle: %w", err)
	}
	return nil
}

func ValidateQAReview(r *QAReview) error {
	if r.VisualScore != nil && *r.VisualScore == 0 {
		r.VisualScore = nil
	}
	if err := validate.Struct(r); err != nil {
		return fmt.Errorf("invalid QAReview: %w", err)
	}
	if r.HasUncertainty && strings.TrimSpace(r.UncertaintyReason) == "" {
		return fmt.Errorf("invalid QAReview: uncertainty_reason is required when has_uncertainty=true")
	}
	return nil
}

// FormatForLinkedIn assembles the final post text respecting LinkedIn's 3000-character limit.
func (d *PostDraft) FormatForLinkedIn() string {
	tags := strings.Join(d.Hashtags, " ")
	parts := []string{
		strings.TrimSpace(d.Hook),
		strings.TrimSpace(d.Body),
		strings.TrimSpace(d.CallToAction),
		tags,
	}
	full := strings.Join(parts, "\n\n")
	if len(full) <= 3000 {
		return full
	}
	return full[:2997] + "..."
}

func PostDraftSchemaJSON() ([]byte, error) {
	return schemaJSON(PostDraft{})
}

func SidequestArticleSchemaJSON() ([]byte, error) {
	return schemaJSON(SidequestArticle{})
}

func QAReviewSchemaJSON() ([]byte, error) {
	return schemaJSON(QAReview{})
}

func schemaJSON(v any) ([]byte, error) {
	r := &jsonschema.Reflector{DoNotReference: true}
	s := r.Reflect(v)
	return json.MarshalIndent(s, "", "  ")
}
