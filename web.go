package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
)

const webAuthCookieName = "spy_web_token"

type WebServer struct {
	store         *MessageStore
	bot           *bot.Bot
	addr          string
	token         string
	maxMediaBytes int64

	server *http.Server
}

type chatMessageView struct {
	MessageID       int
	Sender          string
	At              string
	Text            string
	Caption         string
	PreviousAt      string
	PreviousText    string
	PreviousCaption string
	HasPrevious     bool
	EditCount       int
	MediaType       string
	MediaURL        string
	IsOwner         bool
	IsDeleted       bool
	IsEdited        bool
	ReplyToID       int
	HasMedia        bool
	HasContent      bool
	StatusLabel     string
}

type indexPageData struct {
	Search   string
	Page     int
	HasPrev  bool
	HasNext  bool
	PrevPage int
	NextPage int
	Users    []BotUserSummary
}

type userChatsPageData struct {
	User          BotUserSummary
	UserPath      string
	Search        string
	Page          int
	HasPrev       bool
	HasNext       bool
	PrevPage      int
	NextPage      int
	Conversations []ConversationSummary
}

type chatPageData struct {
	Conversation ConversationSummary
	UserURL      string
	Messages     []chatMessageView
	Page         int
	HasPrev      bool
	HasNext      bool
	PrevPage     int
	NextPage     int
	Limit        int
}

func NewWebServer(store *MessageStore, botClient *bot.Bot, addr, token string, maxMediaBytes int64) *WebServer {
	if strings.TrimSpace(addr) == "" {
		addr = ":8090"
	}
	if maxMediaBytes <= 0 {
		maxMediaBytes = 50 << 20
	}

	ws := &WebServer{
		store:         store,
		bot:           botClient,
		addr:          addr,
		token:         strings.TrimSpace(token),
		maxMediaBytes: maxMediaBytes,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.withAuth(ws.handleIndex))
	mux.HandleFunc("/user/", ws.withAuth(ws.handleUserChats))
	mux.HandleFunc("/chat/", ws.withAuth(ws.handleChat))

	ws.server = &http.Server{
		Addr:              ws.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return ws
}

func (ws *WebServer) Start() error {
	return ws.server.ListenAndServe()
}

func (ws *WebServer) Shutdown(ctx context.Context) error {
	if ws.server == nil {
		return nil
	}
	return ws.server.Shutdown(ctx)
}

func (ws *WebServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, redirected := ws.authorize(w, r)
		if redirected {
			return
		}
		if !allowed {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html><body style="font-family: sans-serif; padding: 24px;"><h2>Доступ закрыт</h2><p>Добавь <code>?token=...</code> к ссылке.</p></body></html>`))
			return
		}
		next(w, r)
	}
}

func (ws *WebServer) authorize(w http.ResponseWriter, r *http.Request) (allowed bool, redirected bool) {
	if ws.token == "" {
		return true, false
	}

	queryToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if queryToken != "" {
		if secureEqual(queryToken, ws.token) {
			http.SetCookie(w, &http.Cookie{
				Name:     webAuthCookieName,
				Value:    ws.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   86400 * 14,
			})

			cleanURL := *r.URL
			q := cleanURL.Query()
			q.Del("token")
			cleanURL.RawQuery = q.Encode()
			http.Redirect(w, r, cleanURL.String(), http.StatusFound)
			return false, true
		}
		return false, false
	}

	if headerToken := strings.TrimSpace(r.Header.Get("X-Spy-Token")); secureEqual(headerToken, ws.token) {
		return true, false
	}

	if cookie, err := r.Cookie(webAuthCookieName); err == nil && secureEqual(cookie.Value, ws.token) {
		return true, false
	}

	return false, false
}

func secureEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (ws *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	search := strings.TrimSpace(r.URL.Query().Get("q"))
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := 30
	offset := (page - 1) * limit

	users, err := ws.store.ListBotUsersPaged(r.Context(), search, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := indexPageData{
		Search:   search,
		Page:     page,
		HasPrev:  page > 1,
		HasNext:  len(users) == limit,
		PrevPage: maxInt(page-1, 1),
		NextPage: page + 1,
		Users:    users,
	}

	if err := indexTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (ws *WebServer) handleUserChats(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/user/"), "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	if strings.Contains(path, "/") {
		http.NotFound(w, r)
		return
	}

	businessConnectionID, err := url.PathUnescape(path)
	if err != nil || strings.TrimSpace(businessConnectionID) == "" {
		http.NotFound(w, r)
		return
	}

	user, found, err := ws.store.BotUserByBusinessConnection(r.Context(), businessConnectionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	search := strings.TrimSpace(r.URL.Query().Get("q"))
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := 30
	offset := (page - 1) * limit

	conversations, err := ws.store.ListConversationsByBusinessConnectionPaged(
		r.Context(),
		businessConnectionID,
		search,
		limit,
		offset,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := userChatsPageData{
		User:          user,
		UserPath:      url.PathEscape(businessConnectionID),
		Search:        search,
		Page:          page,
		HasPrev:       page > 1,
		HasNext:       len(conversations) == limit,
		PrevPage:      maxInt(page-1, 1),
		NextPage:      page + 1,
		Conversations: conversations,
	}

	if err := userChatsTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (ws *WebServer) handleChat(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/chat/"), "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(path, "/")
	conversationID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || conversationID <= 0 {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 3 && parts[1] == "media" {
		ws.handleChatMedia(w, r, conversationID, parts[2])
		return
	}

	if len(parts) > 1 {
		http.NotFound(w, r)
		return
	}

	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 80)
	if limit > 200 {
		limit = 200
	}
	offset := (page - 1) * limit

	conversation, found, err := ws.store.ConversationByID(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	history, err := ws.store.HistoryByConversationPage(r.Context(), conversationID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	revisionsByMessage, err := ws.store.RevisionsByConversation(r.Context(), conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	views := make([]chatMessageView, 0, len(history))
	for _, msg := range history {
		sender := storedSender(msg)
		statusLabel := ""
		if msg.IsDeleted {
			statusLabel = "Удалено"
		} else if msg.EditedAt != nil {
			statusLabel = "Редактировано"
		}

		view := chatMessageView{
			MessageID:   msg.MessageID,
			Sender:      sender,
			At:          msg.MessageDate.Local().Format("02 Jan 2006 15:04"),
			Text:        msg.Text,
			Caption:     msg.Caption,
			MediaType:   msg.MediaType,
			MediaURL:    fmt.Sprintf("/chat/%d/media/%d", conversationID, msg.MessageID),
			IsOwner:     msg.IsOwner,
			IsDeleted:   msg.IsDeleted,
			IsEdited:    msg.EditedAt != nil,
			ReplyToID:   msg.ReplyToMessageID,
			HasMedia:    msg.MediaType != "",
			HasContent:  msg.Text != "" || msg.Caption != "",
			StatusLabel: statusLabel,
		}

		if revisions := revisionsByMessage[msg.MessageID]; len(revisions) > 1 {
			prev := revisions[len(revisions)-2]
			view.HasPrevious = true
			view.PreviousAt = prev.OccurredAt.Local().Format("02 Jan 2006 15:04")
			view.PreviousText = prev.Text
			view.PreviousCaption = prev.Caption
			view.EditCount = len(revisions) - 1
		}
		views = append(views, view)
	}

	data := chatPageData{
		Conversation: conversation,
		UserURL:      "/user/" + url.PathEscape(conversation.BusinessConnection),
		Messages:     views,
		Page:         page,
		HasPrev:      page > 1,
		HasNext:      offset+len(history) < conversation.MessageCount,
		PrevPage:     maxInt(page-1, 1),
		NextPage:     page + 1,
		Limit:        limit,
	}

	if err := chatTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (ws *WebServer) handleChatMedia(w http.ResponseWriter, r *http.Request, conversationID int64, rawMessageID string) {
	messageID, err := strconv.Atoi(rawMessageID)
	if err != nil || messageID <= 0 {
		http.NotFound(w, r)
		return
	}

	msg, found, err := ws.store.GetConversationMedia(r.Context(), conversationID, messageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found || msg.MediaType == "" {
		http.NotFound(w, r)
		return
	}

	if len(msg.MediaBytes) == 0 && msg.MediaFileID != "" && ws.bot != nil {
		downloaded, err := downloadTelegramFileWithRetry(r.Context(), ws.bot, msg.MediaFileID, ws.maxMediaBytes, 4, 250*time.Millisecond)
		if err == nil && len(downloaded.Data) > 0 {
			msg.MediaBytes = downloaded.Data
			if downloaded.Filename != "" {
				msg.MediaFilename = downloaded.Filename
			}
			if downloaded.MIME != "" {
				msg.MediaMIME = downloaded.MIME
			}

			if _, err := ws.store.UpdateConversationMediaPayload(
				r.Context(),
				conversationID,
				messageID,
				msg.MediaFilename,
				msg.MediaMIME,
				msg.MediaBytes,
			); err != nil {
				// Не роняем ответ клиенту из-за ошибки персиста.
			}
		}
	}

	if len(msg.MediaBytes) == 0 {
		http.NotFound(w, r)
		return
	}

	contentType := strings.TrimSpace(msg.MediaMIME)
	if contentType == "" {
		switch msg.MediaType {
		case "photo":
			contentType = "image/jpeg"
		case "video":
			contentType = "video/mp4"
		default:
			contentType = "application/octet-stream"
		}
	}

	filename := msg.MediaFilename
	if filename == "" {
		filename = fmt.Sprintf("media_%d", msg.MessageID)
		if msg.MediaType == "photo" {
			filename += ".jpg"
		}
		if msg.MediaType == "video" {
			filename += ".mp4"
		}
	}
	filename = filepath.Base(filename)
	if filename == "." || filename == "/" {
		filename = "media.bin"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(
		w,
		r,
		filename,
		msg.UpdatedAt,
		bytes.NewReader(msg.MediaBytes),
	)
}

func parsePositiveInt(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var indexTemplate = template.Must(template.New("index").Funcs(template.FuncMap{
	"formatTimePtr": func(t *time.Time) string {
		if t == nil {
			return "n/a"
		}
		return t.Local().Format("02 Jan 2006 15:04")
	},
	"urlQuery": url.QueryEscape,
	"urlPath":  url.PathEscape,
}).Parse(`
<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Dialog Spy Archive</title>
  <style>
    :root {
      --bg: #f2efe8;
      --card: #fffaf1;
      --ink: #1f2a44;
      --muted: #6f7c94;
      --accent: #e4572e;
      --accent-2: #3d7ea6;
      --line: #d7d0bf;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Manrope", "IBM Plex Sans", "Segoe UI", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at 15% 10%, #fff7e2 0, #f2efe8 45%),
        linear-gradient(140deg, #f8f4ec 0%, #ebe4d6 100%);
      min-height: 100vh;
      padding: 20px;
    }
    .wrap { max-width: 1100px; margin: 0 auto; }
    .hero {
      background: linear-gradient(125deg, #1f2a44, #3d7ea6);
      color: #fff;
      border-radius: 22px;
      padding: 22px 24px;
      box-shadow: 0 14px 32px rgba(23, 35, 56, 0.22);
    }
    .hero h1 {
      margin: 0;
      font-family: "Space Grotesk", "Manrope", sans-serif;
      letter-spacing: 0.02em;
      font-size: 1.5rem;
    }
    .hero p { margin: 8px 0 0; opacity: 0.9; }
    .controls {
      margin: 16px 0 20px;
      display: grid;
      grid-template-columns: 1fr auto;
      gap: 10px;
    }
    input[type="text"] {
      width: 100%;
      border: 1px solid var(--line);
      border-radius: 12px;
      padding: 11px 13px;
      font-size: 15px;
      background: #fff;
    }
    button, .btn {
      border: none;
      background: var(--accent);
      color: #fff;
      border-radius: 12px;
      padding: 11px 16px;
      font-weight: 700;
      text-decoration: none;
      display: inline-block;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(290px, 1fr));
      gap: 14px;
    }
    .card {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 18px;
      padding: 14px;
      box-shadow: 0 8px 20px rgba(80, 66, 33, 0.08);
    }
    .title { margin: 0 0 6px; font-size: 1.05rem; }
    .meta { color: var(--muted); font-size: 0.9rem; margin: 0 0 10px; }
    .stats {
      display: flex; gap: 10px; flex-wrap: wrap;
      margin-bottom: 10px;
      font-size: 0.85rem;
    }
    .badge {
      background: #eff4ff;
      color: #2e4a79;
      border-radius: 999px;
      padding: 4px 10px;
      font-weight: 700;
    }
    .preview {
      color: #3d4658;
      font-size: 0.9rem;
      min-height: 2.8em;
      margin-bottom: 10px;
    }
    .pager {
      margin-top: 18px;
      display: flex;
      gap: 10px;
      align-items: center;
    }
    .pager .btn.alt { background: var(--accent-2); }
    .empty {
      margin-top: 16px;
      border: 1px dashed var(--line);
      border-radius: 14px;
      padding: 18px;
      color: var(--muted);
      background: #fff;
    }
    @media (max-width: 640px) {
      body { padding: 12px; }
      .controls { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <section class="hero">
      <h1>Dialog Spy Archive</h1>
      <p>Пользователи бота и их личные досье по чатам.</p>
    </section>

    <form class="controls" method="get" action="/">
      <input type="text" name="q" value="{{.Search}}" placeholder="Поиск по business connection, имени, username или user_id" />
      <button type="submit">Найти</button>
    </form>

    {{if .Users}}
      <section class="grid">
      {{range .Users}}
        <article class="card">
          <h2 class="title">
            {{if .OwnerName}}{{.OwnerName}}{{else}}Пользователь бота{{end}}
            {{if .OwnerUsername}} · @{{.OwnerUsername}}{{end}}
          </h2>
          <p class="meta">
            {{if .OwnerUserID}}user_id {{.OwnerUserID}} · {{end}}
            business {{.BusinessConnection}}
          </p>
          <div class="stats">
            <span class="badge">Личных чатов {{.ConversationsCount}}</span>
            <span class="badge">Сообщения {{.MessageCount}}</span>
            <span class="badge">Медиа {{.MediaCount}}</span>
          </div>
          <p class="preview">{{if .LastPreview}}{{.LastPreview}}{{else}}Нет данных{{end}}</p>
          <p class="meta">Обновлено: {{formatTimePtr .LastMessageAt}}</p>
          <a class="btn" href="/user/{{urlPath .BusinessConnection}}">Открыть чаты</a>
        </article>
      {{end}}
      </section>
    {{else}}
      <div class="empty">Пользователи не найдены.</div>
    {{end}}

    <div class="pager">
      {{if .HasPrev}}
        <a class="btn alt" href="/?q={{urlQuery .Search}}&page={{.PrevPage}}">Назад</a>
      {{end}}
      {{if .HasNext}}
        <a class="btn" href="/?q={{urlQuery .Search}}&page={{.NextPage}}">Вперёд</a>
      {{end}}
    </div>
  </div>
</body>
</html>
`))

var userChatsTemplate = template.Must(template.New("user-chats").Funcs(template.FuncMap{
	"formatTimePtr": func(t *time.Time) string {
		if t == nil {
			return "n/a"
		}
		return t.Local().Format("02 Jan 2006 15:04")
	},
	"urlQuery": url.QueryEscape,
}).Parse(`
<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>User Dossier</title>
  <style>
    :root {
      --bg: #f2efe8;
      --card: #fffaf1;
      --ink: #1f2a44;
      --muted: #6f7c94;
      --accent: #e4572e;
      --accent-2: #3d7ea6;
      --line: #d7d0bf;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Manrope", "IBM Plex Sans", "Segoe UI", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at 15% 10%, #fff7e2 0, #f2efe8 45%),
        linear-gradient(140deg, #f8f4ec 0%, #ebe4d6 100%);
      min-height: 100vh;
      padding: 20px;
    }
    .wrap { max-width: 1100px; margin: 0 auto; }
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      margin-bottom: 14px;
    }
    .hero {
      background: linear-gradient(125deg, #1f2a44, #3d7ea6);
      color: #fff;
      border-radius: 22px;
      padding: 22px 24px;
      box-shadow: 0 14px 32px rgba(23, 35, 56, 0.22);
      margin-bottom: 14px;
    }
    .hero h1 {
      margin: 0;
      font-family: "Space Grotesk", "Manrope", sans-serif;
      letter-spacing: 0.02em;
      font-size: 1.45rem;
    }
    .hero p { margin: 8px 0 0; opacity: 0.92; }
    .btn {
      border: none;
      background: var(--accent);
      color: #fff;
      border-radius: 12px;
      padding: 10px 14px;
      font-weight: 700;
      text-decoration: none;
      display: inline-block;
    }
    .btn.alt { background: var(--accent-2); }
    .controls {
      margin: 16px 0 20px;
      display: grid;
      grid-template-columns: 1fr auto;
      gap: 10px;
    }
    input[type="text"] {
      width: 100%;
      border: 1px solid var(--line);
      border-radius: 12px;
      padding: 11px 13px;
      font-size: 15px;
      background: #fff;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(290px, 1fr));
      gap: 14px;
    }
    .card {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 18px;
      padding: 14px;
      box-shadow: 0 8px 20px rgba(80, 66, 33, 0.08);
    }
    .title { margin: 0 0 6px; font-size: 1.05rem; }
    .meta { color: var(--muted); font-size: 0.9rem; margin: 0 0 10px; }
    .stats {
      display: flex; gap: 10px; flex-wrap: wrap;
      margin-bottom: 10px;
      font-size: 0.85rem;
    }
    .badge {
      background: #eff4ff;
      color: #2e4a79;
      border-radius: 999px;
      padding: 4px 10px;
      font-weight: 700;
    }
    .preview {
      color: #3d4658;
      font-size: 0.9rem;
      min-height: 2.8em;
      margin-bottom: 10px;
    }
    .pager {
      margin-top: 18px;
      display: flex;
      gap: 10px;
      align-items: center;
    }
    .empty {
      margin-top: 16px;
      border: 1px dashed var(--line);
      border-radius: 14px;
      padding: 18px;
      color: var(--muted);
      background: #fff;
    }
    @media (max-width: 640px) {
      body { padding: 12px; }
      .controls { grid-template-columns: 1fr; }
      .topbar { flex-direction: column; align-items: flex-start; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="topbar">
      <a class="btn alt" href="/">← Пользователи</a>
    </div>

    <section class="hero">
      <h1>
        {{if .User.OwnerName}}{{.User.OwnerName}}{{else}}Пользователь бота{{end}}
        {{if .User.OwnerUsername}} · @{{.User.OwnerUsername}}{{end}}
      </h1>
      <p>
        {{if .User.OwnerUserID}}user_id {{.User.OwnerUserID}} · {{end}}
        business {{.User.BusinessConnection}}
      </p>
      <p>Личных чатов: {{.User.ConversationsCount}} · Сообщений: {{.User.MessageCount}} · Медиа: {{.User.MediaCount}}</p>
    </section>

    <form class="controls" method="get" action="/user/{{.UserPath}}">
      <input type="text" name="q" value="{{.Search}}" placeholder="Поиск по имени чата, username или chat_id" />
      <button type="submit">Найти</button>
    </form>

    {{if .Conversations}}
      <section class="grid">
      {{range .Conversations}}
        <article class="card">
          <h2 class="title">{{.ChatTitle}}</h2>
          <p class="meta">#{{.ID}} · chat_id {{.ChatID}} {{if .ChatUsername}} · @{{.ChatUsername}}{{end}}</p>
          <div class="stats">
            <span class="badge">Сообщения {{.MessageCount}}</span>
            <span class="badge">Медиа {{.MediaCount}}</span>
          </div>
          <p class="preview">{{if .LastPreview}}{{.LastPreview}}{{else}}Нет данных{{end}}</p>
          <p class="meta">Обновлено: {{formatTimePtr .LastMessageAt}}</p>
          <a class="btn" href="/chat/{{.ID}}">Открыть досье</a>
        </article>
      {{end}}
      </section>
    {{else}}
      <div class="empty">Чаты не найдены.</div>
    {{end}}

    <div class="pager">
      {{if .HasPrev}}
        <a class="btn alt" href="/user/{{.UserPath}}?q={{urlQuery .Search}}&page={{.PrevPage}}">Назад</a>
      {{end}}
      {{if .HasNext}}
        <a class="btn" href="/user/{{.UserPath}}?q={{urlQuery .Search}}&page={{.NextPage}}">Вперёд</a>
      {{end}}
    </div>
  </div>
</body>
</html>
`))

var chatTemplate = template.Must(template.New("chat").Funcs(template.FuncMap{
	"urlQuery": url.QueryEscape,
}).Parse(`
<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{{.Conversation.ChatTitle}} - dossier</title>
  <style>
    :root {
      --bg: #f6f3ec;
      --ink: #1f2a44;
      --line: #d5ccba;
      --card: #fffdf8;
      --muted: #6f7c94;
      --owner: #e7f4ff;
      --peer: #fff1de;
      --accent: #e4572e;
      --accent2: #3d7ea6;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Manrope", "IBM Plex Sans", "Segoe UI", sans-serif;
      background: linear-gradient(160deg, #efe8da 0%, #f9f7f2 50%, #ece7dd 100%);
      color: var(--ink);
      min-height: 100vh;
      padding: 18px;
    }
    .wrap { max-width: 1100px; margin: 0 auto; }
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      margin-bottom: 14px;
    }
    .btn {
      text-decoration: none;
      border-radius: 10px;
      padding: 8px 14px;
      color: #fff;
      background: var(--accent2);
      font-weight: 700;
      display: inline-block;
    }
    .dossier {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 18px;
      padding: 16px;
      box-shadow: 0 10px 24px rgba(55, 43, 20, 0.08);
      margin-bottom: 16px;
    }
    .dossier h1 {
      margin: 0 0 6px;
      font-family: "Space Grotesk", "Manrope", sans-serif;
      font-size: 1.45rem;
    }
    .meta { color: var(--muted); font-size: 0.92rem; }
    .stats { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 10px; }
    .badge {
      border-radius: 999px;
      background: #ecf6ff;
      color: #2e4a79;
      padding: 5px 11px;
      font-weight: 700;
      font-size: 0.88rem;
    }
    .feed {
      display: flex;
      flex-direction: column;
      gap: 11px;
    }
    .msg {
      max-width: 88%;
      border: 1px solid var(--line);
      border-radius: 14px;
      padding: 10px 12px;
      background: var(--peer);
      box-shadow: 0 6px 16px rgba(55, 40, 22, 0.06);
    }
    .msg.owner {
      margin-left: auto;
      background: var(--owner);
      border-color: #b8d9f2;
    }
    .head {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
      margin-bottom: 7px;
      font-size: 0.83rem;
      color: var(--muted);
    }
    .status {
      color: #9a6432;
      font-weight: 700;
    }
    .body { white-space: pre-wrap; line-height: 1.38; }
    .cap { margin-top: 6px; color: #4d576c; font-size: 0.95rem; white-space: pre-wrap; }
    .reply { margin-top: 5px; font-size: 0.83rem; color: #85653c; }
    .previous {
      margin-top: 8px;
      padding: 8px 10px;
      border-radius: 10px;
      border: 1px dashed #d5b896;
      background: #fff6ea;
      font-size: 0.9rem;
      color: #6b4c25;
    }
    .previous-head {
      font-size: 0.78rem;
      text-transform: uppercase;
      letter-spacing: 0.06em;
      margin-bottom: 5px;
      color: #89623a;
      font-weight: 700;
    }
    .previous-body { white-space: pre-wrap; }
    .previous-cap {
      margin-top: 5px;
      color: #79573a;
      font-size: 0.85rem;
      white-space: pre-wrap;
    }
    .media { margin-top: 8px; }
    img.media-photo {
      width: min(230px, 100%);
      max-height: 230px;
      object-fit: cover;
      border-radius: 12px;
      border: 1px solid #d6c8af;
      display: block;
    }
    video.media-video {
      width: min(300px, 100%);
      max-height: 240px;
      border-radius: 12px;
      border: 1px solid #d6c8af;
      display: block;
      background: #0f1726;
    }
    .pager {
      margin-top: 14px;
      display: flex;
      gap: 10px;
      align-items: center;
    }
    .pager .btn.prev { background: #8e9eb6; }
    .pager .btn.next { background: var(--accent); }
    .empty {
      padding: 18px;
      border: 1px dashed var(--line);
      border-radius: 14px;
      color: var(--muted);
      background: #fff;
    }
    @media (max-width: 780px) {
      .msg { max-width: 100%; }
      body { padding: 12px; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="topbar">
      <a class="btn" href="{{.UserURL}}">← К чатам пользователя</a>
      <div class="meta">Досье #{{.Conversation.ID}}</div>
    </div>

    <section class="dossier">
      <h1>{{.Conversation.ChatTitle}}</h1>
      <div class="meta">
        chat_id {{.Conversation.ChatID}}
        {{if .Conversation.ChatUsername}} · @{{.Conversation.ChatUsername}}{{end}}
        · business {{.Conversation.BusinessConnection}}
      </div>
      <div class="stats">
        <span class="badge">Сообщения {{.Conversation.MessageCount}}</span>
        <span class="badge">Медиа {{.Conversation.MediaCount}}</span>
        <span class="badge">Страница {{.Page}}</span>
      </div>
    </section>

    {{if .Messages}}
    <section class="feed">
      {{range .Messages}}
      <article class="msg {{if .IsOwner}}owner{{end}}">
        <div class="head">
          <span>{{.Sender}} · #{{.MessageID}}</span>
          <span>{{.At}} {{if .StatusLabel}} · <span class="status">{{.StatusLabel}}</span>{{end}}</span>
        </div>
        {{if .Text}}<div class="body">{{.Text}}</div>{{end}}
        {{if .Caption}}<div class="cap">📌 {{.Caption}}</div>{{end}}
        {{if .ReplyToID}}<div class="reply">↪ reply to #{{.ReplyToID}}</div>{{end}}
        {{if .HasPrevious}}
        <div class="previous">
          <div class="previous-head">Предыдущая версия · {{.PreviousAt}} · правок: {{.EditCount}}</div>
          {{if .PreviousText}}<div class="previous-body">{{.PreviousText}}</div>{{end}}
          {{if .PreviousCaption}}<div class="previous-cap">📌 {{.PreviousCaption}}</div>{{end}}
        </div>
        {{end}}
        {{if .HasMedia}}
        <div class="media">
          {{if eq .MediaType "photo"}}
            <img class="media-photo" src="{{.MediaURL}}" loading="lazy" alt="photo" />
          {{else if eq .MediaType "video"}}
            <video class="media-video" controls preload="metadata" src="{{.MediaURL}}"></video>
          {{else}}
            <a href="{{.MediaURL}}">Скачать медиа</a>
          {{end}}
        </div>
        {{end}}
      </article>
      {{end}}
    </section>
    {{else}}
    <div class="empty">Сообщения отсутствуют.</div>
    {{end}}

    <div class="pager">
      {{if .HasPrev}}
        <a class="btn prev" href="/chat/{{.Conversation.ID}}?page={{.PrevPage}}&limit={{.Limit}}">← Назад</a>
      {{end}}
      {{if .HasNext}}
        <a class="btn next" href="/chat/{{.Conversation.ID}}?page={{.NextPage}}&limit={{.Limit}}">Вперёд →</a>
      {{end}}
    </div>
  </div>
</body>
</html>
`))
