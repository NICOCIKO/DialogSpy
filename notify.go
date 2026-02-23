package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	maxMediaBackupBytes = 50 << 20
	maxCaptionLen       = 1000
	maxMessageLen       = 3800
)

func sendNotification(ctx context.Context, b *bot.Bot, userID int64, text string) {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    userID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		log.Printf("failed to send message to chat %d: %v", userID, err)
	}
}

func sendLongNotification(ctx context.Context, b *bot.Bot, userID int64, text string) {
	if len(text) <= maxMessageLen {
		sendNotification(ctx, b, userID, text)
		return
	}

	parts := strings.Split(text, "\n")
	var chunk strings.Builder
	for _, line := range parts {
		next := line + "\n"
		if chunk.Len()+len(next) > maxMessageLen && chunk.Len() > 0 {
			sendNotification(ctx, b, userID, chunk.String())
			chunk.Reset()
		}
		chunk.WriteString(next)
	}

	if chunk.Len() > 0 {
		sendNotification(ctx, b, userID, chunk.String())
	}
}

func sendMediaBackup(
	ctx context.Context,
	b *bot.Bot,
	userID int64,
	mediaType string,
	mediaFileID string,
	caption string,
) error {
	err := sendMediaByFileID(ctx, b, userID, mediaType, mediaFileID, caption)
	if err == nil {
		return nil
	}

	if !shouldRetryMediaAsUpload(err) {
		return err
	}

	return sendMediaByUpload(ctx, b, userID, mediaType, mediaFileID, caption)
}

func sendStoredMedia(
	ctx context.Context,
	b *bot.Bot,
	userID int64,
	msg StoredMessage,
	prefix string,
) error {
	if msg.MediaType == "" {
		return fmt.Errorf("message has no media")
	}

	caption := strings.TrimSpace(prefix)
	if msg.Caption != "" {
		if caption != "" {
			caption += "\n\n"
		}
		caption += msg.Caption
	}
	caption = trimCaption(caption)

	if len(msg.MediaBytes) > 0 {
		filename := msg.MediaFilename
		if filename == "" {
			switch msg.MediaType {
			case "photo":
				filename = "photo.jpg"
			case "video":
				filename = "video.mp4"
			default:
				filename = "file.bin"
			}
		}

		file := &models.InputFileUpload{
			Filename: filename,
			Data:     bytes.NewReader(msg.MediaBytes),
		}

		switch msg.MediaType {
		case "photo":
			_, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
				ChatID:    userID,
				Photo:     file,
				Caption:   caption,
				ParseMode: models.ParseModeHTML,
			})
			return err
		case "video":
			_, err := b.SendVideo(ctx, &bot.SendVideoParams{
				ChatID:            userID,
				Video:             file,
				Caption:           caption,
				ParseMode:         models.ParseModeHTML,
				SupportsStreaming: true,
			})
			return err
		case "file":
			_, err := b.SendDocument(ctx, &bot.SendDocumentParams{
				ChatID:    userID,
				Document:  file,
				Caption:   caption,
				ParseMode: models.ParseModeHTML,
			})
			return err
		default:
			return fmt.Errorf("unsupported media type: %s", msg.MediaType)
		}
	}

	if msg.MediaFileID != "" {
		return sendMediaBackup(ctx, b, userID, msg.MediaType, msg.MediaFileID, caption)
	}

	return fmt.Errorf("no media bytes or media file id")
}

func sendMediaByFileID(
	ctx context.Context,
	b *bot.Bot,
	userID int64,
	mediaType string,
	mediaFileID string,
	caption string,
) error {
	caption = trimCaption(caption)

	switch mediaType {
	case "photo":
		_, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:    userID,
			Photo:     &models.InputFileString{Data: mediaFileID},
			Caption:   caption,
			ParseMode: models.ParseModeHTML,
		})
		return err
	case "video":
		_, err := b.SendVideo(ctx, &bot.SendVideoParams{
			ChatID:            userID,
			Video:             &models.InputFileString{Data: mediaFileID},
			Caption:           caption,
			ParseMode:         models.ParseModeHTML,
			SupportsStreaming: true,
		})
		return err
	case "file":
		_, err := b.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID:    userID,
			Document:  &models.InputFileString{Data: mediaFileID},
			Caption:   caption,
			ParseMode: models.ParseModeHTML,
		})
		return err
	default:
		return fmt.Errorf("unsupported media type: %s", mediaType)
	}
}

func sendMediaByUpload(
	ctx context.Context,
	b *bot.Bot,
	userID int64,
	mediaType string,
	mediaFileID string,
	caption string,
) error {
	downloaded, err := downloadTelegramFileWithRetry(ctx, b, mediaFileID, maxMediaBackupBytes, 4, 250*time.Millisecond)
	if err != nil {
		return err
	}

	upload := &models.InputFileUpload{
		Filename: downloaded.Filename,
		Data:     bytes.NewReader(downloaded.Data),
	}

	caption = trimCaption(caption)

	switch mediaType {
	case "photo":
		_, err = b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:    userID,
			Photo:     upload,
			Caption:   caption,
			ParseMode: models.ParseModeHTML,
		})
		return err
	case "video":
		_, err = b.SendVideo(ctx, &bot.SendVideoParams{
			ChatID:            userID,
			Video:             upload,
			Caption:           caption,
			ParseMode:         models.ParseModeHTML,
			SupportsStreaming: true,
		})
		return err
	case "file":
		_, err = b.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID:    userID,
			Document:  upload,
			Caption:   caption,
			ParseMode: models.ParseModeHTML,
		})
		return err
	default:
		return fmt.Errorf("unsupported media type: %s", mediaType)
	}
}

func shouldRetryMediaAsUpload(err error) bool {
	lowerErr := strings.ToLower(err.Error())
	return strings.Contains(lowerErr, "can't use file of type") ||
		strings.Contains(lowerErr, "selfdestructing")
}

func trimCaption(caption string) string {
	if len(caption) <= maxCaptionLen {
		return caption
	}
	return caption[:maxCaptionLen-1] + "…"
}
