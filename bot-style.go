package main

import (
	"fmt"
	"os"
	"strings"
)

type BotStyle struct {
	Shield string
	Spark  string
	Web    string
	Stats  string
	Chats  string
	Media  string
	Doc    string
	Lock   string
	Hello  string
	Check  string
	Warn   string
}

var botStyle BotStyle

func InitBotStyleFromEnv() {
	botStyle = BotStyle{
		Shield: styleEmoji("EMOJI_SHIELD_ID", "🛡️"),
		Spark:  styleEmoji("EMOJI_SPARK_ID", "✨"),
		Web:    styleEmoji("EMOJI_WEB_ID", "🌐"),
		Stats:  styleEmoji("EMOJI_STATS_ID", "📊"),
		Chats:  styleEmoji("EMOJI_CHATS_ID", "🗂️"),
		Media:  styleEmoji("EMOJI_MEDIA_ID", "🎞️"),
		Doc:    styleEmoji("EMOJI_DOC_ID", "📜"),
		Lock:   styleEmoji("EMOJI_LOCK_ID", "🔒"),
		Hello:  styleEmoji("EMOJI_HELLO_ID", "👋"),
		Check:  styleEmoji("EMOJI_CHECK_ID", "✅"),
		Warn:   styleEmoji("EMOJI_WARN_ID", "⚠️"),
	}
}

func styleEmoji(envKey, fallback string) string {
	id := strings.TrimSpace(os.Getenv(envKey))
	if id == "" {
		return fallback
	}
	return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji>`, id, fallback)
}
