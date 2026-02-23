package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func handleCommandMessage(
	ctx context.Context,
	b *bot.Bot,
	msg *models.Message,
	store *MessageStore,
	access *AccessControl,
	webPublicURL string,
	webToken string,
) {
	text := strings.TrimSpace(msg.Text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return
	}

	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}

	userID := msg.From.ID
	isAdmin := access.IsAdmin(userID)
	if err := store.UpsertSubscriber(ctx, userID, msg.From.Username, fullName(msg.From), isAdmin, userID); err != nil {
		log.Printf("failed to upsert subscriber %d: %v", userID, err)
	}
	command := normalizeCommand(parts[0])
	args := parts[1:]

	if command == "/start" {
		if isAdmin {
			sendNotification(ctx, b, userID, adminStartText())
		} else {
			sendNotification(ctx, b, userID, guestStartText())
		}
		return
	}

	if !isAdmin {
		sendNotification(ctx, b, userID, guestRestrictedText())
		return
	}

	switch command {
	case "/help":
		sendNotification(ctx, b, userID, adminHelpText())
	case "/stats":
		handleStatsCommand(ctx, b, store, userID)
	case "/web":
		handleWebCommand(ctx, b, userID, webPublicURL, webToken)
	case "/chats":
		handleChatsCommand(ctx, b, store, userID, args)
	case "/history":
		handleHistoryCommand(ctx, b, store, userID, args)
	case "/media":
		handleMediaCommand(ctx, b, store, userID, args)
	default:
		sendNotification(
			ctx,
			b,
			userID,
			fmt.Sprintf("%s Неизвестная команда. Нажми /help", botStyle.Warn),
		)
	}
}

func handleWebCommand(
	ctx context.Context,
	b *bot.Bot,
	actorUserID int64,
	webPublicURL string,
	webToken string,
) {
	webPublicURL = strings.TrimSpace(webPublicURL)
	if webPublicURL == "" {
		sendNotification(
			ctx,
			b,
			actorUserID,
			fmt.Sprintf("%s WEB_PUBLIC_URL не задан. Добавь в .env: <code>http://localhost:8090</code>", botStyle.Warn),
		)
		return
	}

	link := webPublicURL
	if webToken != "" {
		parsed, err := url.Parse(webPublicURL)
		if err == nil {
			q := parsed.Query()
			q.Set("token", webToken)
			parsed.RawQuery = q.Encode()
			link = parsed.String()
		}
	}

	sendNotification(
		ctx,
		b,
		actorUserID,
		fmt.Sprintf("%s <b>Веб-интерфейс досье</b>\n<code>%s</code>", botStyle.Web, escapeHTML(link)),
	)
}

func handleStatsCommand(ctx context.Context, b *bot.Bot, store *MessageStore, actorUserID int64) {
	messageCount, err := store.Count(ctx)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения статистики: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}

	conversationCount, err := store.CountConversations(ctx)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения статистики: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}

	sendNotification(
		ctx,
		b,
		actorUserID,
		fmt.Sprintf(
			"%s <b>Статистика архива</b>\n━━━━━━━━━━━━━━━\nДиалогов: <b>%d</b>\nСообщений: <b>%d</b>",
			botStyle.Stats,
			conversationCount,
			messageCount,
		),
	)
}

func handleChatsCommand(
	ctx context.Context,
	b *bot.Bot,
	store *MessageStore,
	actorUserID int64,
	args []string,
) {
	limit := 20
	if len(args) > 0 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed <= 0 {
			sendNotification(ctx, b, actorUserID, "Использование: <code>/chats [limit]</code>")
			return
		}
		limit = parsed
	}

	conversations, err := store.ListConversations(ctx, limit)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения диалогов: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}
	if len(conversations) == 0 {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Диалогов в архиве пока нет.", botStyle.Chats))
		return
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("%s <b>Диалоги в архиве</b>\n", botStyle.Chats))
	builder.WriteString("━━━━━━━━━━━━━━━\n")
	builder.WriteString(fmt.Sprintf("Показано: <b>%d</b>\n\n", len(conversations)))

	for _, conv := range conversations {
		builder.WriteString(fmt.Sprintf(
			"<b>#%d</b> %s\n"+
				"Chat ID: <code>%d</code>\n"+
				"Сообщений: <b>%d</b> | Медиа: <b>%d</b>\n"+
				"Обновлено: <code>%s</code>\n",
			conv.ID,
			escapeHTML(conv.ChatTitle),
			conv.ChatID,
			conv.MessageCount,
			conv.MediaCount,
			formatTimePtr(conv.LastMessageAt),
		))
		if conv.LastPreview != "" {
			builder.WriteString(fmt.Sprintf("Последнее: <i>%s</i>\n", escapeHTML(conv.LastPreview)))
		}
		builder.WriteString(fmt.Sprintf(
			"<code>/history %d 30</code>  <code>/media %d 10</code>\n",
			conv.ID,
			conv.ID,
		))
		builder.WriteString("━━━━━━━━━━━━━━━\n")
	}

	sendLongNotification(ctx, b, actorUserID, builder.String())
}

func handleHistoryCommand(
	ctx context.Context,
	b *bot.Bot,
	store *MessageStore,
	actorUserID int64,
	args []string,
) {
	if len(args) == 0 {
		sendNotification(ctx, b, actorUserID, "Использование: <code>/history &lt;conversation_id&gt; [limit]</code>")
		return
	}

	conversationID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || conversationID <= 0 {
		sendNotification(ctx, b, actorUserID, "conversation_id должен быть положительным числом")
		return
	}

	limit := 30
	if len(args) > 1 {
		parsed, err := strconv.Atoi(args[1])
		if err != nil || parsed <= 0 {
			sendNotification(ctx, b, actorUserID, "limit должен быть положительным числом")
			return
		}
		limit = parsed
	}

	conversation, found, err := store.ConversationByID(ctx, conversationID)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения диалога: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}
	if !found {
		sendNotification(ctx, b, actorUserID, "Диалог не найден")
		return
	}

	history, err := store.HistoryByConversation(ctx, conversationID, limit)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения истории: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}
	if len(history) == 0 {
		sendNotification(ctx, b, actorUserID, "В этом диалоге пока нет сообщений")
		return
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(
		"%s <b>История #%d</b> %s\n━━━━━━━━━━━━━━━\n",
		botStyle.Doc,
		conversation.ID,
		escapeHTML(conversation.ChatTitle),
	))
	builder.WriteString(fmt.Sprintf(
		"Сообщений в диалоге: <b>%d</b> | Показано: <b>%d</b>\n",
		conversation.MessageCount,
		len(history),
	))
	builder.WriteString("━━━━━━━━━━━━━━━\n")

	for _, item := range history {
		builder.WriteString(fmt.Sprintf(
			"🕒 <code>%s</code>  <b>%s</b>  <code>#%d</code>\n",
			item.MessageDate.Local().Format("02.01 15:04"),
			escapeHTML(storedSender(item)),
			item.MessageID,
		))

		if item.IsDeleted {
			builder.WriteString("<i>Удалено</i>\n")
		}
		if item.EditedAt != nil {
			builder.WriteString("<i>Редактировалось</i>\n")
		}
		if item.Text != "" {
			builder.WriteString(escapeHTML(item.Text))
			builder.WriteString("\n")
		}
		if item.Caption != "" {
			builder.WriteString("📌 ")
			builder.WriteString(escapeHTML(item.Caption))
			builder.WriteString("\n")
		}
		if item.MediaType != "" {
			builder.WriteString("📎 ")
			builder.WriteString(escapeHTML(mediaTypeLabel(item.MediaType)))
			builder.WriteString("\n")
		}
		if item.ReplyToMessageID > 0 {
			builder.WriteString(fmt.Sprintf("↪️ reply to <code>#%d</code>\n", item.ReplyToMessageID))
		}
		builder.WriteString("━━━━━━━━━━━━━━━\n")
	}

	sendLongNotification(ctx, b, actorUserID, builder.String())
}

func handleMediaCommand(
	ctx context.Context,
	b *bot.Bot,
	store *MessageStore,
	actorUserID int64,
	args []string,
) {
	if len(args) == 0 {
		sendNotification(ctx, b, actorUserID, "Использование: <code>/media &lt;conversation_id&gt; [limit]</code>")
		return
	}

	conversationID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || conversationID <= 0 {
		sendNotification(ctx, b, actorUserID, "conversation_id должен быть положительным числом")
		return
	}

	limit := 10
	if len(args) > 1 {
		parsed, err := strconv.Atoi(args[1])
		if err != nil || parsed <= 0 {
			sendNotification(ctx, b, actorUserID, "limit должен быть положительным числом")
			return
		}
		limit = parsed
	}

	conversation, found, err := store.ConversationByID(ctx, conversationID)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения диалога: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}
	if !found {
		sendNotification(ctx, b, actorUserID, "Диалог не найден")
		return
	}

	items, err := store.MediaByConversation(ctx, conversationID, limit)
	if err != nil {
		sendNotification(ctx, b, actorUserID, fmt.Sprintf("%s Ошибка чтения медиа: <code>%s</code>", botStyle.Warn, escapeHTML(err.Error())))
		return
	}
	if len(items) == 0 {
		sendNotification(ctx, b, actorUserID, "В этом диалоге нет медиа")
		return
	}

	sendNotification(
		ctx,
		b,
		actorUserID,
		fmt.Sprintf(
			"%s <b>Медиа архив #%d</b> %s\nПоказано: <b>%d</b>",
			botStyle.Media,
			conversation.ID,
			escapeHTML(conversation.ChatTitle),
			len(items),
		),
	)

	for _, item := range items {
		prefix := fmt.Sprintf(
			"<b>#%d</b> • <code>#%d</code>\n<code>%s</code> • %s",
			conversation.ID,
			item.MessageID,
			item.MessageDate.Local().Format("02.01.2006 15:04"),
			escapeHTML(storedSender(item)),
		)

		if err := sendStoredMedia(ctx, b, actorUserID, item, prefix); err != nil {
			sendNotification(
				ctx,
				b,
				actorUserID,
				fmt.Sprintf(
					"%s Ошибка отправки медиа #<code>%d</code>: <code>%s</code>",
					botStyle.Warn,
					item.MessageID,
					escapeHTML(err.Error()),
				),
			)
		}
	}
}

func adminStartText() string {
	return strings.TrimSpace(fmt.Sprintf(
		`%s <b>Control Center</b>
━━━━━━━━━━━━━━━
%s Ты вошёл как <b>администратор</b>.
Используй /help для списка команд.`,
		botStyle.Shield,
		botStyle.Check,
	))
}

func guestStartText() string {
	return strings.TrimSpace(fmt.Sprintf(
		`%s <b>Привет!</b>
Этот бот работает в режиме мониторинга бизнес-диалогов.
%s Для обычных пользователей доступна только команда <code>/start</code>.`,
		botStyle.Hello,
		botStyle.Lock,
	))
}

func guestRestrictedText() string {
	return fmt.Sprintf(
		`%s <b>Доступ ограничен</b>
Для вашего аккаунта доступна только команда <code>/start</code>.`,
		botStyle.Lock,
	)
}

func adminHelpText() string {
	return strings.TrimSpace(fmt.Sprintf(
		`%s <b>Команды архива</b>
━━━━━━━━━━━━━━━
<code>/start</code> - приветствие и статус доступа
<code>/stats</code> - общая статистика БД
<code>/web</code> - ссылка на веб-интерфейс
<code>/chats [limit]</code> - список диалогов
<code>/history &lt;conversation_id&gt; [limit]</code> - история сообщений
<code>/media &lt;conversation_id&gt; [limit]</code> - последние фото/видео/файлы

Пример:
<code>/chats 20</code>
<code>/history 3 50</code>
<code>/media 3 10</code>`,
		botStyle.Spark,
	))
}

func normalizeCommand(raw string) string {
	cmd := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	return cmd
}

func storedSender(item StoredMessage) string {
	if item.IsOwner {
		return "Вы"
	}
	if item.FromUsername != "" {
		return "@" + item.FromUsername
	}
	if item.FromName != "" {
		return item.FromName
	}
	if item.FromUserID != 0 {
		return fmt.Sprintf("User %d", item.FromUserID)
	}
	return "Unknown"
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "n/a"
	}
	return t.Local().Format("02.01.2006 15:04")
}
