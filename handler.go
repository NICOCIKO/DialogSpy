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
	// ───────────────── COMMANDS ─────────────────
	if update.Message != nil && update.Message.Text != "" {
		if update.Message.From != nil {
			handleCommandMessage(ctx, b, update.Message, store, access, webPublicURL, webToken)
		}
		return
	}

	// ───────────── BUSINESS CONNECTION ─────────────
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

	// ───────────── NEW BUSINESS MESSAGE ─────────────
	if update.BusinessMessage != nil {
		msg := update.BusinessMessage

		// ✅ ВСЕГДА сохраняем (админу нужно)
		if err := saveMessageSnapshot(ctx, b, store, msg, "created", mediaMaxBytes); err != nil {
			log.Printf("failed to save business message: %v", err)
		}

		// ✅ Backup исчезающих медиа по reply (оставляем)
		if isBusinessOwnerUser(ctx, store, msg.BusinessConnectionID, msg.Chat.ID, msg.From) {
			maybeBackupMediaOnReply(ctx, b, msg, store, access, mediaMaxBytes)
		}

		// ❗ ГЛАВНОЕ: НЕ уведомляем о новых сообщениях
		return
	}

	// ───────────── EDITED MESSAGE ─────────────
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

	// ───────────── DELETED MESSAGES ─────────────
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

			// 📝 текст
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

			// 📦 медиа
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
