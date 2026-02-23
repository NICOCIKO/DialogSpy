package main

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
)

func getChatTitle(chat models.Chat) string {
	if chat.Title != "" {
		return chat.Title
	}
	if chat.Username != "" {
		return "@" + chat.Username
	}
	name := chat.FirstName
	if chat.LastName != "" {
		name += " " + chat.LastName
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("Chat %d", chat.ID)
}

func getUserName(user *models.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	name := user.FirstName
	if user.LastName != "" {
		name += " " + user.LastName
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("User %d", user.ID)
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}
