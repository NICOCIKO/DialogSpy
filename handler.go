package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func handleUpdate(
	ctx context.Context,
	b *bot.Bot,
	update *models.Update,
	store *MessageStore,
	access *AccessControl,
	mediaMaxBytes int64,
	webPublicURL string,
	webToken string,
) {
	if update.Message != nil && update.Message.Text != "" {
		if update.Message.From != nil {
			handleCommandMessage(ctx, b, update.Message, store, access, webPublicURL, webToken)
		}
		return
	}

	if update.BusinessConnection != nil {
		bc := update.BusinessConnection
		connectedAt := time.Now().UTC()
		if bc.Date > 0 {
			connectedAt = time.Unix(bc.Date, 0).UTC()
		}

		if err := store.UpsertBusinessAccount(
			ctx,
			bc.ID,
			bc.User.ID,
			bc.User.Username,
			fullName(&bc.User),
			bc.UserChatID,
			bc.IsEnabled,
			connectedAt,
		); err != nil {
			log.Printf("failed to upsert business account %s: %v", bc.ID, err)
		}

		if err := store.UpsertSubscriber(
			ctx,
			bc.User.ID,
			bc.User.Username,
			fullName(&bc.User),
			access.IsAdmin(bc.User.ID),
			bc.UserChatID,
		); err != nil {
			log.Printf("failed to upsert business subscriber %d: %v", bc.User.ID, err)
		}
		return
	}

	if update.BusinessMessage != nil {
		msg := update.BusinessMessage

		if err := saveMessageSnapshot(ctx, b, store, msg, "created", mediaMaxBytes); err != nil {
			log.Printf("failed to save business message: %v", err)
		}

		if isBusinessOwnerUser(ctx, store, msg.BusinessConnectionID, msg.Chat.ID, msg.From) {
			maybeBackupMediaOnReply(ctx, b, msg, store, access, mediaMaxBytes)
		}
		return
	}

	if update.EditedBusinessMessage != nil {
		edited := update.EditedBusinessMessage
		chatTitle := getChatTitle(edited.Chat)
		userName := getUserName(edited.From)

		original, exists, err := store.Get(
			ctx,
			edited.BusinessConnectionID,
			edited.Chat.ID,
			edited.ID,
		)
		if err != nil {
			log.Printf("failed to load original message: %v", err)
		}

		if err := saveMessageSnapshot(ctx, b, store, edited, "edited", mediaMaxBytes); err != nil {
			log.Printf("failed to save edited message: %v", err)
		}

		originalText := messageMainContent(original.Text, original.Caption)
		editedText := messageMainContent(edited.Text, edited.Caption)

		var notification string
		if err == nil && exists && originalText != "" {
			if originalText == editedText {
				notification = fmt.Sprintf(
					"✏️ <b>%s</b> | %s\n"+
						"━━━━━━━━━━━━━━━\n"+
						"<i>Сообщение отредактировано (текст не изменился)</i>",
					userName,
					chatTitle,
				)
			} else {
				diffHTML := generatePrettyDiff(originalText, editedText)
				notification = fmt.Sprintf(
					"✏️ <b>%s</b> | %s\n"+
						"━━━━━━━━━━━━━━━\n"+
						"%s",
					userName,
					chatTitle,
					diffHTML,
				)
			}
		} else {
			fallbackText := editedText
			if fallbackText == "" {
				if mediaType, _ := extractMediaFromMessage(edited); mediaType != "" {
					fallbackText = "Медиа сообщение обновлено"
				}
			}

			notification = fmt.Sprintf(
				"✏️ <b>%s</b> | %s\n"+
					"━━━━━━━━━━━━━━━\n"+
					"%s",
				userName,
				chatTitle,
				escapeHTML(fallbackText),
			)
		}

		notifyRecipientsByConnection(ctx, b, store, edited.BusinessConnectionID, notification)
		return
	}

	if update.DeletedBusinessMessages != nil {
		deleted := update.DeletedBusinessMessages
		bizConnID := deleted.BusinessConnectionID
		chatID := deleted.Chat.ID
		chatTitle := getChatTitle(deleted.Chat)
		now := time.Now().UTC()
		recipientIDs := recipientIDsByConnection(ctx, store, bizConnID)

		for _, messageID := range deleted.MessageIDs {
			original, exists, err := store.MarkDeleted(ctx, bizConnID, chatID, messageID, now)
			if err != nil {
				log.Printf("failed to mark message as deleted: %v", err)
				continue
			}
			if !exists {
				continue
			}

			if original.Text != "" {
				notification := fmt.Sprintf(
					"🗑 <b>%s</b>\n"+
						"━━━━━━━━━━━━━━━\n"+
						"%s",
					chatTitle,
					escapeHTML(original.Text),
				)
				notifyUserIDs(ctx, b, recipientIDs, notification)
			}

			if original.MediaType != "" {
				prefix := fmt.Sprintf(
					"🗑 <b>%s</b>\n<b>Удалено:</b> %s\n<b>От:</b> %s\n<b>Сообщение:</b> <code>#%d</code>",
					escapeHTML(chatTitle),
					escapeHTML(mediaTypeLabel(original.MediaType)),
					escapeHTML(storedSender(original)),
					original.MessageID,
				)

				delivered := false
				var lastErr error
				for _, userID := range recipientIDs {
					if err := sendStoredMedia(ctx, b, userID, original, prefix); err != nil {
						lastErr = err
						continue
					}
					delivered = true
				}
				if delivered {
					continue
				}

				notification := fmt.Sprintf(
					"🗑 <b>%s</b>\n"+
						"━━━━━━━━━━━━━━━\n"+
						"<i>Удалено %s</i>",
					chatTitle,
					mediaTypeLabel(original.MediaType),
				)
				if original.Caption != "" {
					notification += "\n" + escapeHTML(original.Caption)
				}
				if lastErr != nil {
					notification += "\n\n" + fmt.Sprintf(
						"%s Не удалось отправить медиа: <code>%s</code>",
						botStyle.Warn,
						escapeHTML(lastErr.Error()),
					)
				}
				notifyUserIDs(ctx, b, recipientIDs, notification)
			}
		}
	}
}

func saveMessageSnapshot(
	ctx context.Context,
	b *bot.Bot,
	store *MessageStore,
	msg *models.Message,
	eventType string,
	mediaMaxBytes int64,
) error {
	mediaType, mediaFileID, mediaFilename, mediaMIME := extractMediaMetaFromMessage(msg)
	var mediaBytes []byte

	if mediaType != "" && mediaFileID != "" {
		downloaded, err := downloadTelegramFileWithRetry(ctx, b, mediaFileID, mediaMaxBytes, 4, 250*time.Millisecond)
		if err != nil {
			log.Printf("media download skipped (message_id=%d): %v", msg.ID, err)
		} else {
			mediaFilename = downloaded.Filename
			mediaMIME = downloaded.MIME
			mediaBytes = downloaded.Data
		}
	}

	eventTime := time.Now().UTC()
	if eventType == "edited" && msg.EditDate > 0 {
		eventTime = time.Unix(int64(msg.EditDate), 0).UTC()
	} else if msg.Date > 0 {
		eventTime = time.Unix(int64(msg.Date), 0).UTC()
	}

	replyToMessageID := 0
	if msg.ReplyToMessage != nil {
		replyToMessageID = msg.ReplyToMessage.ID
	}
	isOwner := isBusinessOwnerUser(ctx, store, msg.BusinessConnectionID, msg.Chat.ID, msg.From)

	snapshot := MessageSnapshot{
		BusinessConnectionID: msg.BusinessConnectionID,
		ChatID:               msg.Chat.ID,
		ChatTitle:            getChatTitle(msg.Chat),
		ChatUsername:         msg.Chat.Username,
		MessageID:            msg.ID,
		FromUserID:           userID(msg.From),
		FromUsername:         username(msg.From),
		FromName:             fullName(msg.From),
		IsOwner:              isOwner,
		Text:                 msg.Text,
		Caption:              msg.Caption,
		MediaType:            mediaType,
		MediaFileID:          mediaFileID,
		MediaFilename:        mediaFilename,
		MediaMIME:            mediaMIME,
		MediaBytes:           mediaBytes,
		ReplyToMessageID:     replyToMessageID,
		EventTime:            eventTime,
	}

	return store.SaveMessage(ctx, snapshot, eventType)
}

func maybeBackupMediaOnReply(
	ctx context.Context,
	b *bot.Bot,
	msg *models.Message,
	store *MessageStore,
	access *AccessControl,
	mediaMaxBytes int64,
) {
	if msg.ReplyToMessage == nil {
		return
	}

	repliedID := msg.ReplyToMessage.ID
	if repliedID == 0 {
		return
	}

	stored, exists, err := store.Get(ctx, msg.BusinessConnectionID, msg.Chat.ID, repliedID)
	if err != nil {
		log.Printf("failed to load replied message from db: %v", err)
		return
	}
	if exists && stored.BackedUp {
		return
	}

	mediaType := stored.MediaType
	mediaFileID := stored.MediaFileID
	mediaCaption := stored.Caption

	if mediaFileID == "" {
		mediaType, mediaFileID = extractMediaFromMessage(msg.ReplyToMessage)
		mediaCaption = msg.ReplyToMessage.Caption
	}
	if mediaFileID == "" || mediaType == "" {
		return
	}
	if mediaType != "photo" && mediaType != "video" {
		return
	}
	// По reply сохраняем только исчезающие фото/видео.
	// Обычные фото/видео (без protected content) игнорируем.
	if !msg.ReplyToMessage.HasProtectedContent {
		return
	}

	backupMessage := stored
	backupMessage.MediaType = mediaType
	backupMessage.MediaFileID = mediaFileID
	if backupMessage.Caption == "" {
		backupMessage.Caption = mediaCaption
	}

	if len(backupMessage.MediaBytes) == 0 && backupMessage.MediaFileID != "" {
		downloaded, err := downloadTelegramFileWithRetry(ctx, b, backupMessage.MediaFileID, mediaMaxBytes, 4, 250*time.Millisecond)
		if err != nil {
			log.Printf("reply media download skipped (message_id=%d): %v", repliedID, err)
		} else {
			backupMessage.MediaBytes = downloaded.Data
			backupMessage.MediaFilename = downloaded.Filename
			backupMessage.MediaMIME = downloaded.MIME

			if _, err := store.UpdateMediaPayload(
				ctx,
				msg.BusinessConnectionID,
				msg.Chat.ID,
				repliedID,
				downloaded.Filename,
				downloaded.MIME,
				downloaded.Data,
			); err != nil {
				log.Printf("failed to persist reply media bytes: %v", err)
			}
		}
	}

	if !exists {
		replyToMessageID := 0
		if msg.ReplyToMessage != nil && msg.ReplyToMessage.ReplyToMessage != nil {
			replyToMessageID = msg.ReplyToMessage.ReplyToMessage.ID
		}

		eventTime := time.Now().UTC()
		if msg.ReplyToMessage != nil && msg.ReplyToMessage.Date > 0 {
			eventTime = time.Unix(int64(msg.ReplyToMessage.Date), 0).UTC()
		}

		snapshot := MessageSnapshot{
			BusinessConnectionID: msg.BusinessConnectionID,
			ChatID:               msg.Chat.ID,
			ChatTitle:            getChatTitle(msg.Chat),
			ChatUsername:         msg.Chat.Username,
			MessageID:            repliedID,
			FromUserID:           userID(msg.ReplyToMessage.From),
			FromUsername:         username(msg.ReplyToMessage.From),
			FromName:             fullName(msg.ReplyToMessage.From),
			IsOwner:              isBusinessOwnerUser(ctx, store, msg.BusinessConnectionID, msg.Chat.ID, msg.ReplyToMessage.From),
			Text:                 msg.ReplyToMessage.Text,
			Caption:              backupMessage.Caption,
			MediaType:            backupMessage.MediaType,
			MediaFileID:          backupMessage.MediaFileID,
			MediaFilename:        backupMessage.MediaFilename,
			MediaMIME:            backupMessage.MediaMIME,
			MediaBytes:           backupMessage.MediaBytes,
			ReplyToMessageID:     replyToMessageID,
			EventTime:            eventTime,
		}

		if err := store.SaveMessage(ctx, snapshot, "reply_backup"); err != nil {
			log.Printf("failed to create replied message snapshot for backup: %v", err)
		} else {
			exists = true
		}
	}

	prefix := fmt.Sprintf(
		"💾 <b>Сохранено по reply</b>\n<b>Чат:</b> %s\n<b>Тип:</b> %s",
		escapeHTML(getChatTitle(msg.Chat)),
		mediaTypeLabel(mediaType),
	)

	recipientIDs := recipientIDsByConnection(ctx, store, msg.BusinessConnectionID)
	if len(recipientIDs) == 0 && msg.From != nil && msg.From.ID > 0 {
		recipientIDs = append(recipientIDs, msg.From.ID)
	}
	delivered := false
	var lastErr error
	for _, userID := range recipientIDs {
		if err := sendStoredMedia(ctx, b, userID, backupMessage, prefix); err != nil {
			lastErr = err
			continue
		}
		delivered = true
	}
	if !delivered {
		errText := "unknown error"
		if lastErr != nil {
			errText = lastErr.Error()
		}
		notifyRecipientsByConnection(
			ctx,
			b,
			store,
			msg.BusinessConnectionID,
			fmt.Sprintf("%s Не удалось сохранить медиа: <code>%s</code>", botStyle.Warn, escapeHTML(errText)),
		)
		return
	}

	if exists {
		if _, err := store.MarkBackedUp(ctx, msg.BusinessConnectionID, msg.Chat.ID, repliedID); err != nil {
			log.Printf("failed to mark message as backed up: %v", err)
		}
	}

	notifyRecipientsByConnection(
		ctx,
		b,
		store,
		msg.BusinessConnectionID,
		fmt.Sprintf(
			"%s Сохранено по reply: %s (%s)",
			botStyle.Check,
			mediaTypeLabel(mediaType),
			escapeHTML(getChatTitle(msg.Chat)),
		),
	)
}

func notifyUserIDs(ctx context.Context, b *bot.Bot, userIDs []int64, text string) {
	for _, userID := range userIDs {
		sendNotification(ctx, b, userID, text)
	}
}

func recipientIDsByConnection(ctx context.Context, store *MessageStore, businessConnectionID string) []int64 {
	ids, err := store.RecipientChatIDsByBusinessConnection(ctx, businessConnectionID)
	if err != nil {
		log.Printf("failed to resolve recipients for business connection %s: %v", businessConnectionID, err)
		return nil
	}
	return ids
}

func notifyRecipientsByConnection(
	ctx context.Context,
	b *bot.Bot,
	store *MessageStore,
	businessConnectionID string,
	text string,
) {
	notifyUserIDs(ctx, b, recipientIDsByConnection(ctx, store, businessConnectionID), text)
}

func isBusinessOwnerUser(
	ctx context.Context,
	store *MessageStore,
	businessConnectionID string,
	chatID int64,
	from *models.User,
) bool {
	if from == nil || strings.TrimSpace(businessConnectionID) == "" {
		return false
	}

	ownerID, found, err := store.BusinessOwnerID(ctx, businessConnectionID)
	if err != nil {
		log.Printf("failed to resolve business owner for %s: %v", businessConnectionID, err)
		return false
	}
	if !found {
		// Fallback for old connections without BusinessConnection update yet:
		// in business private chats customer messages usually have from.id == chat.id.
		if chatID != 0 && from.ID != 0 {
			return from.ID != chatID
		}
		return false
	}

	return from.ID == ownerID
}

func extractMediaFromMessage(msg *models.Message) (string, string) {
	mediaType, mediaFileID, _, _ := extractMediaMetaFromMessage(msg)
	return mediaType, mediaFileID
}

func extractMediaMetaFromMessage(msg *models.Message) (string, string, string, string) {
	if len(msg.Photo) > 0 {
		return "photo", msg.Photo[len(msg.Photo)-1].FileID, "photo.jpg", "image/jpeg"
	}
	if msg.Video != nil {
		return "video", msg.Video.FileID, msg.Video.FileName, msg.Video.MimeType
	}
	if msg.Document != nil {
		mediaType := detectMediaType(msg.Document.MimeType, msg.Document.FileName)
		if mediaType == "" {
			mediaType = "file"
		}
		return mediaType, msg.Document.FileID, msg.Document.FileName, msg.Document.MimeType
	}
	if msg.VideoNote != nil {
		return "video", msg.VideoNote.FileID, "video_note.mp4", "video/mp4"
	}
	if msg.Animation != nil {
		return "video", msg.Animation.FileID, msg.Animation.FileName, msg.Animation.MimeType
	}
	if msg.Audio != nil {
		filename := strings.TrimSpace(msg.Audio.FileName)
		if filename == "" {
			filename = "audio"
		}
		return "file", msg.Audio.FileID, filename, msg.Audio.MimeType
	}
	if msg.Voice != nil {
		return "file", msg.Voice.FileID, "voice.ogg", msg.Voice.MimeType
	}
	return "", "", "", ""
}

func detectMediaType(mimeType string, fileName string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "photo"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".heic":
		return "photo"
	case ".mp4", ".mov", ".m4v", ".webm":
		return "video"
	default:
		return ""
	}
}

func messageMainContent(text, caption string) string {
	if text != "" {
		return text
	}
	return caption
}

func mediaTypeLabel(mediaType string) string {
	switch mediaType {
	case "photo":
		return "фото"
	case "video":
		return "видео"
	case "file":
		return "файл"
	default:
		return "медиа"
	}
}

func userID(user *models.User) int64 {
	if user == nil {
		return 0
	}
	return user.ID
}

func username(user *models.User) string {
	if user == nil {
		return ""
	}
	return user.Username
}

func fullName(user *models.User) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name != "" {
		return name
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	return fmt.Sprintf("User %d", user.ID)
}
