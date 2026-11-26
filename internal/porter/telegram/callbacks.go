package telegram

import (
	"context"
	"strings"

	"codeberg.org/matheus/porter/internal/settings"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (s *BotService) handleCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	ok, _, err := s.IsAuthorized(ctx, update.CallbackQuery.From.ID)
	if err != nil || !ok {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            "Unauthorized",
			ShowAlert:       true,
		})
		return
	}

	data := update.CallbackQuery.Data
	switch {
	case strings.HasPrefix(data, "toggle:"):
		key := strings.TrimPrefix(data, "toggle:")
		cur, _ := s.settings.GetBool(ctx, key)
		next := "true"
		if cur {
			next = "false"
		}
		if err := s.settings.Set(ctx, key, next); err != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: update.CallbackQuery.ID,
				Text:            "Failed: " + err.Error(),
				ShowAlert:       true,
			})
			return
		}
		if s.onSettingChanged != nil {
			s.onSettingChanged(key)
		}
		text, kb, err := s.buildConfigView(ctx)
		if err == nil && update.CallbackQuery.Message.Message != nil {
			_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        text,
				ReplyMarkup: kb,
			})
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            key + " = " + next,
		})

	case strings.HasPrefix(data, "tone:"):
		tone := strings.TrimPrefix(data, "tone:")
		if err := s.settings.Set(ctx, settings.KeyWriterTone, tone); err != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: update.CallbackQuery.ID,
				Text:            "Failed: " + err.Error(),
				ShowAlert:       true,
			})
			return
		}
		text, kb := s.buildToneView(ctx)
		if update.CallbackQuery.Message.Message != nil {
			_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        text,
				ReplyMarkup: kb,
			})
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            "Tone set to " + tone,
		})

	case strings.HasPrefix(data, "post:"):
		parts := strings.SplitN(strings.TrimPrefix(data, "post:"), ":", 2)
		if len(parts) != 2 {
			return
		}
		action, postID := parts[0], parts[1]
		if err := s.intake.HandlePostAction(ctx, action, postID); err != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: update.CallbackQuery.ID,
				Text:            err.Error(),
				ShowAlert:       true,
			})
			return
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            "Action received: " + action,
		})
	}
}
