package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"codeberg.org/matheus/porter/internal/porter/linkedin"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// IntakeHandler decouples Telegram message/callback delivery from Odradek/Orchestrator.
type IntakeHandler interface {
	HandleTextIdea(ctx context.Context, text string) error
	HandleLink(ctx context.Context, url string) error
	HandleVoice(ctx context.Context, fileID string) error
	HandlePostAction(ctx context.Context, action, postID string) error
	HandleRunNow(ctx context.Context) error
	HandleWeeklyDevlog(ctx context.Context) error
}

type noopIntake struct{}

func (noopIntake) HandleTextIdea(context.Context, string) error {
	return fmt.Errorf("Odradek pipeline not wired yet (Milestone 3/5)")
}
func (noopIntake) HandleLink(context.Context, string) error {
	return fmt.Errorf("Odradek pipeline not wired yet (Milestone 3/5)")
}
func (noopIntake) HandleVoice(context.Context, string) error {
	return fmt.Errorf("Odradek audio curator not wired yet (Milestone 3)")
}
func (noopIntake) HandlePostAction(context.Context, string, string) error {
	return fmt.Errorf("Orchestrator actions not wired yet (Milestone 5)")
}
func (noopIntake) HandleRunNow(context.Context) error {
	return fmt.Errorf("Orchestrator not wired yet")
}
func (noopIntake) HandleWeeklyDevlog(context.Context) error {
	return fmt.Errorf("Orchestrator not wired yet")
}

type BotService struct {
	repo             *store.Repository
	settings         *settings.Manager
	oauth            *linkedin.OAuthService
	intake           IntakeHandler
	onSettingChanged func(key string)
	logger           *slog.Logger

	mu     sync.RWMutex
	client *bot.Bot
}

func NewBotService(
	repo *store.Repository,
	mgr *settings.Manager,
	oauth *linkedin.OAuthService,
	logger *slog.Logger,
) *BotService {
	if logger == nil {
		logger = slog.Default()
	}
	return &BotService{
		repo:     repo,
		settings: mgr,
		oauth:    oauth,
		intake:   noopIntake{},
		logger:   logger,
	}
}

func (s *BotService) SetIntakeHandler(h IntakeHandler) {
	if h != nil {
		s.intake = h
	}
}

func (s *BotService) SetOnSettingChanged(fn func(key string)) {
	s.onSettingChanged = fn
}

// Start initializes the Telegram bot with the configured token and blocks until ctx is canceled.
func (s *BotService) Start(ctx context.Context, token string) error {
	b, err := bot.New(token,
		bot.WithDefaultHandler(s.handleDefault),
	)
	if err != nil {
		return fmt.Errorf("init telegram bot: %w", err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypePrefix, s.handleStart)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypePrefix, s.handleHelp)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/config", bot.MatchTypePrefix, s.withAdmin(s.handleConfig))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set", bot.MatchTypePrefix, s.withAdmin(s.handleSet))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/tone", bot.MatchTypePrefix, s.withAdmin(s.handleTone))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/week", bot.MatchTypePrefix, s.withAdmin(s.handleWeek))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add_theme", bot.MatchTypePrefix, s.withAdmin(s.handleAddTheme))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/patterns", bot.MatchTypePrefix, s.withAdmin(s.handlePatterns))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/trust", bot.MatchTypePrefix, s.withAdmin(s.handleTrust))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/run", bot.MatchTypePrefix, s.withAdmin(s.handleRun))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/devlog", bot.MatchTypePrefix, s.withAdmin(s.handleDevlog))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auth", bot.MatchTypePrefix, s.withAdmin(s.handleAuth))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, s.handleCallback)

	s.mu.Lock()
	s.client = b
	s.mu.Unlock()

	s.logger.Info("porter telegram bot started")
	b.Start(ctx)
	return nil
}

// IsAuthorized checks whether senderID matches the configured admin chat ID.
func (s *BotService) IsAuthorized(ctx context.Context, senderID int64) (authorized bool, configured bool, err error) {
	adminID, err := s.settings.GetInt64(ctx, settings.KeyTelegramAdminChatID)
	if err != nil {
		return false, false, err
	}
	if adminID == 0 {
		return false, false, nil
	}
	return senderID == adminID, true, nil
}

func (s *BotService) withAdmin(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		var senderID int64
		var chatID int64
		if update.Message != nil && update.Message.From != nil {
			senderID = update.Message.From.ID
			chatID = update.Message.Chat.ID
		} else if update.CallbackQuery != nil {
			senderID = update.CallbackQuery.From.ID
			if update.CallbackQuery.Message.Message != nil {
				chatID = update.CallbackQuery.Message.Message.Chat.ID
			}
		}
		ok, configured, err := s.IsAuthorized(ctx, senderID)
		if err != nil {
			s.logger.Error("auth check failed", "err", err)
			return
		}
		if !ok {
			if !configured && chatID != 0 {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: chatID,
					Text: fmt.Sprintf(
						"🔒 Admin Chat ID is not configured yet.\nYour Chat ID is: `%d`\nRun:\n`porter config set telegram.admin_chat_id %d`",
						senderID, senderID,
					),
				})
			}
			return
		}
		next(ctx, b, update)
	}
}

func (s *BotService) SendAlert(ctx context.Context, text string) error {
	s.mu.RLock()
	b := s.client
	s.mu.RUnlock()
	if b == nil {
		return fmt.Errorf("telegram bot not connected")
	}
	adminID, err := s.settings.GetInt64(ctx, settings.KeyTelegramAdminChatID)
	if err != nil || adminID == 0 {
		return fmt.Errorf("admin_chat_id not configured")
	}
	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: adminID,
		Text:   text,
	})
	return err
}

func (s *BotService) SendPreview(ctx context.Context, postID, imagePath, title, fullText, uncertaintyReason string) (int, error) {
	s.mu.RLock()
	b := s.client
	s.mu.RUnlock()
	if b == nil {
		return 0, fmt.Errorf("telegram bot not connected")
	}
	adminID, err := s.settings.GetInt64(ctx, settings.KeyTelegramAdminChatID)
	if err != nil || adminID == 0 {
		return 0, fmt.Errorf("admin_chat_id not configured")
	}

	if imagePath != "" {
		f, err := os.Open(imagePath)
		if err == nil {
			defer f.Close()
			caption := title
			if len(caption) > 1000 {
				caption = caption[:1000]
			}
			_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{
				ChatID:  adminID,
				Photo:   &models.InputFileUpload{Filename: "card.png", Data: f},
				Caption: caption,
			})
		}
	}

	var sb strings.Builder
	if uncertaintyReason != "" {
		sb.WriteString("⚠️ Odradek flagged uncertainty:\n")
		sb.WriteString(uncertaintyReason)
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString(fullText)

	kb := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "✅ Aprovar e Postar", CallbackData: "post:approve:" + postID},
				{Text: "🛡 Aprovar + Confiar", CallbackData: "post:trust:" + postID},
			},
			{
				{Text: "🔄 Regerar", CallbackData: "post:regen:" + postID},
				{Text: "✏️ Editar", CallbackData: "post:edit:" + postID},
				{Text: "🗑 Descartar", CallbackData: "post:discard:" + postID},
			},
		},
	}

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      adminID,
		Text:        sb.String(),
		ReplyMarkup: kb,
	})
	if err != nil {
		return 0, err
	}
	return msg.ID, nil
}

// DownloadFile downloads a Telegram file (e.g., voice note .ogg) by its file_id.
func (s *BotService) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	s.mu.RLock()
	b := s.client
	s.mu.RUnlock()
	if b == nil {
		return nil, fmt.Errorf("telegram bot not connected")
	}
	f, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get telegram file: %w", err)
	}
	fileURL := b.FileDownloadLink(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
}
