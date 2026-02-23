package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MessageSnapshot struct {
	BusinessConnectionID string
	ChatID               int64
	ChatTitle            string
	ChatUsername         string
	MessageID            int
	FromUserID           int64
	FromUsername         string
	FromName             string
	IsOwner              bool
	Text                 string
	Caption              string
	MediaType            string
	MediaFileID          string
	MediaFilename        string
	MediaMIME            string
	MediaBytes           []byte
	ReplyToMessageID     int
	EventTime            time.Time
}

type StoredMessage struct {
	ConversationID       int64
	BusinessConnectionID string
	ChatID               int64
	ChatTitle            string
	MessageID            int
	FromUserID           int64
	FromUsername         string
	FromName             string
	IsOwner              bool
	Text                 string
	Caption              string
	MediaType            string
	MediaFileID          string
	MediaFilename        string
	MediaMIME            string
	MediaBytes           []byte
	ReplyToMessageID     int
	BackedUp             bool
	IsDeleted            bool
	MessageDate          time.Time
	FirstSeenAt          time.Time
	UpdatedAt            time.Time
	EditedAt             *time.Time
	DeletedAt            *time.Time
}

type ConversationSummary struct {
	ID                 int64
	BusinessConnection string
	ChatID             int64
	ChatTitle          string
	ChatUsername       string
	MessageCount       int
	MediaCount         int
	LastMessageAt      *time.Time
	LastPreview        string
}

type BotUserSummary struct {
	BusinessConnection string
	OwnerUserID        int64
	OwnerUsername      string
	OwnerName          string
	ConversationsCount int
	MessageCount       int
	MediaCount         int
	LastMessageAt      *time.Time
	LastPreview        string
}

type MessageRevision struct {
	MessageID  int
	EventType  string
	Text       string
	Caption    string
	OccurredAt time.Time
}

type MessageStore struct {
	db *pgxpool.Pool
}

func NewMessageStore(ctx context.Context, databaseURL string) (*MessageStore, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	store := &MessageStore{db: pool}
	if err := store.initSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return store, nil
}

func (ms *MessageStore) Close() {
	if ms != nil && ms.db != nil {
		ms.db.Close()
	}
}

func (ms *MessageStore) initSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS conversations (
			id BIGSERIAL PRIMARY KEY,
			business_connection_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			chat_title TEXT NOT NULL,
			chat_username TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (business_connection_id, chat_id)
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id BIGSERIAL PRIMARY KEY,
			conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			business_connection_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			message_id INT NOT NULL,
			from_user_id BIGINT,
			from_username TEXT,
			from_name TEXT,
			is_owner BOOLEAN NOT NULL DEFAULT FALSE,
			text TEXT NOT NULL DEFAULT '',
			caption TEXT NOT NULL DEFAULT '',
			media_type TEXT,
			media_file_id TEXT,
			media_filename TEXT,
			media_mime TEXT,
			media_bytes BYTEA,
			reply_to_message_id INT,
			backed_up BOOLEAN NOT NULL DEFAULT FALSE,
			is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
			message_date TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			edited_at TIMESTAMPTZ,
			deleted_at TIMESTAMPTZ,
			UNIQUE (business_connection_id, chat_id, message_id)
		)`,
		`CREATE TABLE IF NOT EXISTS message_events (
			id BIGSERIAL PRIMARY KEY,
			conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
			business_connection_id TEXT NOT NULL,
			chat_id BIGINT NOT NULL,
			message_id INT NOT NULL,
			event_type TEXT NOT NULL,
			actor_user_id BIGINT,
			text TEXT NOT NULL DEFAULT '',
			caption TEXT NOT NULL DEFAULT '',
			media_type TEXT,
			media_file_id TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS business_accounts (
			business_connection_id TEXT PRIMARY KEY,
			owner_user_id BIGINT NOT NULL,
			owner_username TEXT,
			owner_name TEXT,
			owner_chat_id BIGINT,
			is_enabled BOOLEAN NOT NULL DEFAULT TRUE,
			connected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS bot_subscribers (
			user_id BIGINT PRIMARY KEY,
			username TEXT,
			full_name TEXT,
			delivery_chat_id BIGINT,
			is_admin BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE bot_subscribers ADD COLUMN IF NOT EXISTS delivery_chat_id BIGINT`,
		`UPDATE bot_subscribers
		SET delivery_chat_id = user_id
		WHERE delivery_chat_id IS NULL OR delivery_chat_id = 0`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation_updated ON messages (conversation_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation_message_date ON messages (conversation_id, message_date DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_pending_media ON messages (updated_at DESC) WHERE media_type IS NOT NULL AND media_file_id IS NOT NULL AND media_bytes IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_message_events_conversation_created ON message_events (conversation_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations (updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_business_accounts_owner_user_id ON business_accounts (owner_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_business_accounts_last_seen_at ON business_accounts (last_seen_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_bot_subscribers_last_seen_at ON bot_subscribers (last_seen_at DESC)`,
	}

	for _, stmt := range stmts {
		if _, err := ms.db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("init schema failed: %w", err)
		}
	}

	return nil
}

func (ms *MessageStore) SaveMessage(ctx context.Context, snapshot MessageSnapshot, eventType string) error {
	if snapshot.BusinessConnectionID == "" {
		return errors.New("empty business connection id")
	}
	if snapshot.ChatID == 0 {
		return errors.New("empty chat id")
	}
	if snapshot.MessageID == 0 {
		return errors.New("empty message id")
	}
	if snapshot.ChatTitle == "" {
		snapshot.ChatTitle = fmt.Sprintf("Chat %d", snapshot.ChatID)
	}
	if snapshot.EventTime.IsZero() {
		snapshot.EventTime = time.Now().UTC()
	}

	tx, err := ms.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var conversationID int64
	if err := tx.QueryRow(
		ctx,
		`INSERT INTO conversations (
			business_connection_id,
			chat_id,
			chat_title,
			chat_username,
			updated_at
		)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (business_connection_id, chat_id)
		DO UPDATE SET
			chat_title = EXCLUDED.chat_title,
			chat_username = COALESCE(EXCLUDED.chat_username, conversations.chat_username),
			updated_at = NOW()
		RETURNING id`,
		snapshot.BusinessConnectionID,
		snapshot.ChatID,
		snapshot.ChatTitle,
		nullString(snapshot.ChatUsername),
	).Scan(&conversationID); err != nil {
		return err
	}

	editedAt := any(nil)
	if eventType == "edited" {
		editedAt = snapshot.EventTime
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO messages (
			conversation_id,
			business_connection_id,
			chat_id,
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			media_bytes,
			reply_to_message_id,
			message_date,
			updated_at,
			edited_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16,
			$17, NOW(), $18
		)
		ON CONFLICT (business_connection_id, chat_id, message_id)
		DO UPDATE SET
			conversation_id = EXCLUDED.conversation_id,
			from_user_id = EXCLUDED.from_user_id,
			from_username = COALESCE(EXCLUDED.from_username, messages.from_username),
			from_name = COALESCE(EXCLUDED.from_name, messages.from_name),
			is_owner = EXCLUDED.is_owner,
			text = EXCLUDED.text,
			caption = EXCLUDED.caption,
			media_type = COALESCE(EXCLUDED.media_type, messages.media_type),
			media_file_id = COALESCE(EXCLUDED.media_file_id, messages.media_file_id),
			media_filename = COALESCE(EXCLUDED.media_filename, messages.media_filename),
			media_mime = COALESCE(EXCLUDED.media_mime, messages.media_mime),
			media_bytes = COALESCE(EXCLUDED.media_bytes, messages.media_bytes),
			reply_to_message_id = COALESCE(EXCLUDED.reply_to_message_id, messages.reply_to_message_id),
			is_deleted = FALSE,
			deleted_at = NULL,
			updated_at = NOW(),
			edited_at = COALESCE(EXCLUDED.edited_at, messages.edited_at),
			message_date = EXCLUDED.message_date`,
		conversationID,
		snapshot.BusinessConnectionID,
		snapshot.ChatID,
		snapshot.MessageID,
		nullInt64(snapshot.FromUserID),
		nullString(snapshot.FromUsername),
		nullString(snapshot.FromName),
		snapshot.IsOwner,
		snapshot.Text,
		snapshot.Caption,
		nullString(snapshot.MediaType),
		nullString(snapshot.MediaFileID),
		nullString(snapshot.MediaFilename),
		nullString(snapshot.MediaMIME),
		nullBytes(snapshot.MediaBytes),
		nullInt(snapshot.ReplyToMessageID),
		snapshot.EventTime,
		editedAt,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO message_events (
			conversation_id,
			business_connection_id,
			chat_id,
			message_id,
			event_type,
			actor_user_id,
			text,
			caption,
			media_type,
			media_file_id,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		conversationID,
		snapshot.BusinessConnectionID,
		snapshot.ChatID,
		snapshot.MessageID,
		eventType,
		nullInt64(snapshot.FromUserID),
		snapshot.Text,
		snapshot.Caption,
		nullString(snapshot.MediaType),
		nullString(snapshot.MediaFileID),
		snapshot.EventTime,
	); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return nil
}

func (ms *MessageStore) Get(ctx context.Context, businessConnectionID string, chatID int64, messageID int) (StoredMessage, bool, error) {
	row := ms.db.QueryRow(
		ctx,
		`SELECT
			conversation_id,
			business_connection_id,
			chat_id,
			COALESCE((SELECT chat_title FROM conversations WHERE id = messages.conversation_id), ''),
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			media_bytes,
			reply_to_message_id,
			backed_up,
			is_deleted,
			message_date,
			first_seen_at,
			updated_at,
			edited_at,
			deleted_at
		FROM messages
		WHERE business_connection_id = $1 AND chat_id = $2 AND message_id = $3
		LIMIT 1`,
		businessConnectionID, chatID, messageID,
	)

	msg, err := scanStoredMessage(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredMessage{}, false, nil
		}
		return StoredMessage{}, false, err
	}

	return msg, true, nil
}

func (ms *MessageStore) MarkDeleted(ctx context.Context, businessConnectionID string, chatID int64, messageID int, eventTime time.Time) (StoredMessage, bool, error) {
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}

	tx, err := ms.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return StoredMessage{}, false, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	row := tx.QueryRow(
		ctx,
		`UPDATE messages
		SET is_deleted = TRUE, deleted_at = $4, updated_at = NOW()
		WHERE business_connection_id = $1 AND chat_id = $2 AND message_id = $3
		RETURNING
			conversation_id,
			business_connection_id,
			chat_id,
			COALESCE((SELECT chat_title FROM conversations WHERE id = messages.conversation_id), ''),
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			media_bytes,
			reply_to_message_id,
			backed_up,
			is_deleted,
			message_date,
			first_seen_at,
			updated_at,
			edited_at,
			deleted_at`,
		businessConnectionID, chatID, messageID, eventTime,
	)

	msg, err := scanStoredMessage(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredMessage{}, false, nil
		}
		return StoredMessage{}, false, err
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO message_events (
			conversation_id,
			business_connection_id,
			chat_id,
			message_id,
			event_type,
			actor_user_id,
			text,
			caption,
			media_type,
			media_file_id,
			created_at
		)
		VALUES ($1, $2, $3, $4, 'deleted', $5, $6, $7, $8, $9, $10)`,
		msg.ConversationID,
		msg.BusinessConnectionID,
		msg.ChatID,
		msg.MessageID,
		nullInt64(msg.FromUserID),
		msg.Text,
		msg.Caption,
		nullString(msg.MediaType),
		nullString(msg.MediaFileID),
		eventTime,
	); err != nil {
		return StoredMessage{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return StoredMessage{}, false, err
	}

	return msg, true, nil
}

func (ms *MessageStore) MarkBackedUp(ctx context.Context, businessConnectionID string, chatID int64, messageID int) (bool, error) {
	tag, err := ms.db.Exec(
		ctx,
		`UPDATE messages
		SET backed_up = TRUE, updated_at = NOW()
		WHERE business_connection_id = $1 AND chat_id = $2 AND message_id = $3`,
		businessConnectionID,
		chatID,
		messageID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (ms *MessageStore) Count(ctx context.Context) (int, error) {
	var total int
	if err := ms.db.QueryRow(ctx, `SELECT COUNT(*) FROM messages`).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (ms *MessageStore) CountConversations(ctx context.Context) (int, error) {
	var total int
	if err := ms.db.QueryRow(ctx, `SELECT COUNT(*) FROM conversations`).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (ms *MessageStore) RecalculateOwnerFlags(ctx context.Context) (int64, error) {
	var updated int64

	tag, err := ms.db.Exec(
		ctx,
		`UPDATE messages m
		SET is_owner = (m.from_user_id = ba.owner_user_id)
		FROM business_accounts ba
		WHERE m.business_connection_id = ba.business_connection_id
			AND m.from_user_id IS NOT NULL
			AND m.is_owner IS DISTINCT FROM (m.from_user_id = ba.owner_user_id)`,
	)
	if err != nil {
		return 0, err
	}
	updated += tag.RowsAffected()

	tag, err = ms.db.Exec(
		ctx,
		`UPDATE messages m
		SET is_owner = (m.from_user_id <> m.chat_id)
		WHERE m.from_user_id IS NOT NULL
			AND NOT EXISTS (
				SELECT 1
				FROM business_accounts ba
				WHERE ba.business_connection_id = m.business_connection_id
			)
			AND m.is_owner IS DISTINCT FROM (m.from_user_id <> m.chat_id)`,
	)
	if err != nil {
		return 0, err
	}
	updated += tag.RowsAffected()

	return updated, nil
}

func (ms *MessageStore) UpsertBusinessAccount(
	ctx context.Context,
	businessConnectionID string,
	ownerUserID int64,
	ownerUsername string,
	ownerName string,
	ownerChatID int64,
	isEnabled bool,
	connectedAt time.Time,
) error {
	if strings.TrimSpace(businessConnectionID) == "" {
		return errors.New("empty business connection id")
	}
	if ownerUserID <= 0 {
		return errors.New("invalid owner user id")
	}
	if connectedAt.IsZero() {
		connectedAt = time.Now().UTC()
	}

	_, err := ms.db.Exec(
		ctx,
		`INSERT INTO business_accounts (
			business_connection_id,
			owner_user_id,
			owner_username,
			owner_name,
			owner_chat_id,
			is_enabled,
			connected_at,
			updated_at,
			last_seen_at
		)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, 0), $6, $7, NOW(), NOW())
		ON CONFLICT (business_connection_id)
		DO UPDATE SET
			owner_user_id = EXCLUDED.owner_user_id,
			owner_username = COALESCE(NULLIF(EXCLUDED.owner_username, ''), business_accounts.owner_username),
			owner_name = COALESCE(NULLIF(EXCLUDED.owner_name, ''), business_accounts.owner_name),
			owner_chat_id = COALESCE(NULLIF(EXCLUDED.owner_chat_id, 0), business_accounts.owner_chat_id),
			is_enabled = EXCLUDED.is_enabled,
			connected_at = LEAST(business_accounts.connected_at, EXCLUDED.connected_at),
			updated_at = NOW(),
			last_seen_at = NOW()`,
		strings.TrimSpace(businessConnectionID),
		ownerUserID,
		strings.TrimSpace(ownerUsername),
		strings.TrimSpace(ownerName),
		ownerChatID,
		isEnabled,
		connectedAt,
	)
	return err
}

func (ms *MessageStore) BusinessOwnerID(ctx context.Context, businessConnectionID string) (int64, bool, error) {
	row := ms.db.QueryRow(
		ctx,
		`SELECT owner_user_id
		FROM business_accounts
		WHERE business_connection_id = $1
		LIMIT 1`,
		strings.TrimSpace(businessConnectionID),
	)

	var ownerUserID int64
	if err := row.Scan(&ownerUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}

	if ownerUserID <= 0 {
		return 0, false, nil
	}
	return ownerUserID, true, nil
}

func (ms *MessageStore) RecipientChatIDsByBusinessConnection(ctx context.Context, businessConnectionID string) ([]int64, error) {
	businessConnectionID = strings.TrimSpace(businessConnectionID)
	if businessConnectionID == "" {
		return nil, nil
	}

	var ownerUserID int64
	var ownerChatID *int64
	row := ms.db.QueryRow(
		ctx,
		`SELECT
			ba.owner_user_id,
			NULLIF(ba.owner_chat_id, 0) AS owner_chat_id
		FROM business_accounts ba
		WHERE ba.business_connection_id = $1
			AND ba.is_enabled = TRUE
		LIMIT 1`,
		businessConnectionID,
	)

	if err := row.Scan(&ownerUserID, &ownerChatID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			ownerRow := ms.db.QueryRow(
				ctx,
				`SELECT from_user_id
				FROM messages
				WHERE business_connection_id = $1
					AND is_owner = TRUE
					AND from_user_id IS NOT NULL
				ORDER BY updated_at DESC, id DESC
				LIMIT 1`,
				businessConnectionID,
			)
			if err := ownerRow.Scan(&ownerUserID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, nil
				}
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	if ownerUserID <= 0 {
		return nil, nil
	}

	var subscriberChatID *int64
	subRow := ms.db.QueryRow(
		ctx,
		`SELECT NULLIF(delivery_chat_id, 0)
		FROM bot_subscribers
		WHERE user_id = $1
		LIMIT 1`,
		ownerUserID,
	)
	if err := subRow.Scan(&subscriberChatID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	targets := make([]int64, 0, 3)
	appendUnique := func(id int64) {
		if id <= 0 {
			return
		}
		for _, existing := range targets {
			if existing == id {
				return
			}
		}
		targets = append(targets, id)
	}

	// Primary target: standard private chat with bot (user_id).
	appendUnique(ownerUserID)
	if subscriberChatID != nil {
		appendUnique(*subscriberChatID)
	}
	if ownerChatID != nil {
		appendUnique(*ownerChatID)
	}

	return targets, nil
}

func (ms *MessageStore) UpsertSubscriber(
	ctx context.Context,
	userID int64,
	username string,
	fullName string,
	isAdmin bool,
	deliveryChatID int64,
) error {
	if userID <= 0 {
		return errors.New("invalid user id")
	}
	if deliveryChatID <= 0 {
		deliveryChatID = userID
	}

	_, err := ms.db.Exec(
		ctx,
		`INSERT INTO bot_subscribers (
			user_id,
			username,
			full_name,
			delivery_chat_id,
			is_admin,
			updated_at,
			last_seen_at
		)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, NOW(), NOW())
		ON CONFLICT (user_id)
		DO UPDATE SET
			username = COALESCE(NULLIF(EXCLUDED.username, ''), bot_subscribers.username),
			full_name = COALESCE(NULLIF(EXCLUDED.full_name, ''), bot_subscribers.full_name),
			delivery_chat_id = COALESCE(NULLIF(EXCLUDED.delivery_chat_id, 0), bot_subscribers.delivery_chat_id, bot_subscribers.user_id),
			is_admin = bot_subscribers.is_admin OR EXCLUDED.is_admin,
			updated_at = NOW(),
			last_seen_at = NOW()`,
		userID,
		strings.TrimSpace(username),
		strings.TrimSpace(fullName),
		isAdmin,
		deliveryChatID,
	)
	return err
}

func (ms *MessageStore) ListSubscriberIDs(ctx context.Context) ([]int64, error) {
	rows, err := ms.db.Query(
		ctx,
		`SELECT COALESCE(NULLIF(delivery_chat_id, 0), user_id) AS target_chat_id
		FROM bot_subscribers
		ORDER BY is_admin DESC, last_seen_at DESC, user_id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]int64, 0, 16)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		if userID > 0 {
			out = append(out, userID)
		}
	}

	return out, rows.Err()
}

func (ms *MessageStore) PurgePhotoBytesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, errors.New("cutoff time is zero")
	}

	tag, err := ms.db.Exec(
		ctx,
		`UPDATE messages
		SET media_bytes = NULL
		WHERE media_type = 'photo'
			AND media_bytes IS NOT NULL
			AND first_seen_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}

func (ms *MessageStore) ListBotUsersPaged(
	ctx context.Context,
	search string,
	limit int,
	offset int,
) ([]BotUserSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	searchPattern := "%"
	if trimmed := strings.TrimSpace(search); trimmed != "" {
		searchPattern = "%" + strings.ToLower(trimmed) + "%"
	}

	rows, err := ms.db.Query(
		ctx,
		`SELECT
			u.business_connection_id,
			COALESCE(ba.owner_user_id, owner.from_user_id) AS owner_user_id,
			COALESCE(NULLIF(ba.owner_username, ''), owner.from_username, '') AS from_username,
			COALESCE(NULLIF(ba.owner_name, ''), owner.from_name, '') AS from_name,
			COALESCE(stats.conversations_count, 0) AS conversations_count,
			COALESCE(stats.message_count, 0) AS message_count,
			COALESCE(stats.media_count, 0) AS media_count,
			stats.last_message_at,
			COALESCE(last_message.preview, '') AS preview
		FROM (
			SELECT business_connection_id
			FROM conversations
			UNION
			SELECT business_connection_id
			FROM business_accounts
		) AS u
		LEFT JOIN business_accounts ba
			ON ba.business_connection_id = u.business_connection_id
		LEFT JOIN LATERAL (
			SELECT
				m.from_user_id,
				m.from_username,
				m.from_name
			FROM messages m
			JOIN conversations c ON c.id = m.conversation_id
			WHERE c.business_connection_id = u.business_connection_id
				AND m.is_owner = TRUE
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS owner ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				COUNT(DISTINCT c.id) AS conversations_count,
				COUNT(m.id) AS message_count,
				COUNT(m.id) FILTER (
					WHERE m.media_type IS NOT NULL
				) AS media_count,
				MAX(m.updated_at) AS last_message_at
			FROM conversations c
			LEFT JOIN messages m ON m.conversation_id = c.id
			WHERE c.business_connection_id = u.business_connection_id
		) AS stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				CASE
					WHEN m.is_deleted THEN '[deleted]'
					WHEN m.text <> '' THEN LEFT(m.text, 80)
					WHEN m.caption <> '' THEN LEFT(m.caption, 80)
					WHEN m.media_type IS NOT NULL THEN '[' || m.media_type || ']'
					ELSE '[empty]'
				END AS preview
			FROM messages m
			JOIN conversations c ON c.id = m.conversation_id
			WHERE c.business_connection_id = u.business_connection_id
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS last_message ON TRUE
		WHERE (
			$1 = '%'
			OR LOWER(u.business_connection_id) LIKE $1
			OR LOWER(COALESCE(NULLIF(ba.owner_username, ''), owner.from_username, '')) LIKE $1
			OR LOWER(COALESCE(NULLIF(ba.owner_name, ''), owner.from_name, '')) LIKE $1
			OR CAST(COALESCE(ba.owner_user_id, owner.from_user_id, 0) AS TEXT) LIKE REPLACE($1, '%', '')
		)
		ORDER BY stats.last_message_at DESC NULLS LAST, u.business_connection_id DESC
		LIMIT $2 OFFSET $3`,
		searchPattern,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BotUserSummary
	for rows.Next() {
		var item BotUserSummary
		var ownerUserID *int64
		var conversationsCount int64
		var messageCount int64
		var mediaCount int64

		if err := rows.Scan(
			&item.BusinessConnection,
			&ownerUserID,
			&item.OwnerUsername,
			&item.OwnerName,
			&conversationsCount,
			&messageCount,
			&mediaCount,
			&item.LastMessageAt,
			&item.LastPreview,
		); err != nil {
			return nil, err
		}

		if ownerUserID != nil {
			item.OwnerUserID = *ownerUserID
		}
		item.ConversationsCount = int(conversationsCount)
		item.MessageCount = int(messageCount)
		item.MediaCount = int(mediaCount)
		out = append(out, item)
	}

	return out, rows.Err()
}

func (ms *MessageStore) BotUserByBusinessConnection(
	ctx context.Context,
	businessConnectionID string,
) (BotUserSummary, bool, error) {
	row := ms.db.QueryRow(
		ctx,
		`SELECT
			u.business_connection_id,
			COALESCE(ba.owner_user_id, owner.from_user_id) AS owner_user_id,
			COALESCE(NULLIF(ba.owner_username, ''), owner.from_username, '') AS from_username,
			COALESCE(NULLIF(ba.owner_name, ''), owner.from_name, '') AS from_name,
			COALESCE(stats.conversations_count, 0) AS conversations_count,
			COALESCE(stats.message_count, 0) AS message_count,
			COALESCE(stats.media_count, 0) AS media_count,
			stats.last_message_at,
			COALESCE(last_message.preview, '') AS preview
		FROM (
			SELECT business_connection_id
			FROM business_accounts
			WHERE business_connection_id = $1
			UNION
			SELECT business_connection_id
			FROM conversations
			WHERE business_connection_id = $1
		) AS u
		LEFT JOIN business_accounts ba
			ON ba.business_connection_id = u.business_connection_id
		LEFT JOIN LATERAL (
			SELECT
				m.from_user_id,
				m.from_username,
				m.from_name
			FROM messages m
			JOIN conversations c ON c.id = m.conversation_id
			WHERE c.business_connection_id = u.business_connection_id
				AND m.is_owner = TRUE
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS owner ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				COUNT(DISTINCT c.id) AS conversations_count,
				COUNT(m.id) AS message_count,
				COUNT(m.id) FILTER (
					WHERE m.media_type IS NOT NULL
				) AS media_count,
				MAX(m.updated_at) AS last_message_at
			FROM conversations c
			LEFT JOIN messages m ON m.conversation_id = c.id
			WHERE c.business_connection_id = u.business_connection_id
		) AS stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				CASE
					WHEN m.is_deleted THEN '[deleted]'
					WHEN m.text <> '' THEN LEFT(m.text, 80)
					WHEN m.caption <> '' THEN LEFT(m.caption, 80)
					WHEN m.media_type IS NOT NULL THEN '[' || m.media_type || ']'
					ELSE '[empty]'
				END AS preview
			FROM messages m
			JOIN conversations c ON c.id = m.conversation_id
			WHERE c.business_connection_id = u.business_connection_id
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS last_message ON TRUE
		LIMIT 1`,
		strings.TrimSpace(businessConnectionID),
	)

	var item BotUserSummary
	var ownerUserID *int64
	var conversationsCount int64
	var messageCount int64
	var mediaCount int64

	err := row.Scan(
		&item.BusinessConnection,
		&ownerUserID,
		&item.OwnerUsername,
		&item.OwnerName,
		&conversationsCount,
		&messageCount,
		&mediaCount,
		&item.LastMessageAt,
		&item.LastPreview,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BotUserSummary{}, false, nil
		}
		return BotUserSummary{}, false, err
	}

	if ownerUserID != nil {
		item.OwnerUserID = *ownerUserID
	}
	item.ConversationsCount = int(conversationsCount)
	item.MessageCount = int(messageCount)
	item.MediaCount = int(mediaCount)
	return item, true, nil
}

func (ms *MessageStore) ListConversations(ctx context.Context, limit int) ([]ConversationSummary, error) {
	return ms.ListConversationsPaged(ctx, "", limit, 0)
}

func (ms *MessageStore) ListConversationsByBusinessConnectionPaged(
	ctx context.Context,
	businessConnectionID string,
	search string,
	limit int,
	offset int,
) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	searchPattern := "%"
	if trimmed := strings.TrimSpace(search); trimmed != "" {
		searchPattern = "%" + strings.ToLower(trimmed) + "%"
	}

	rows, err := ms.db.Query(
		ctx,
		`SELECT
			c.id,
			c.business_connection_id,
			c.chat_id,
			c.chat_title,
			COALESCE(c.chat_username, ''),
			COALESCE(stats.message_count, 0) AS message_count,
			COALESCE(stats.media_count, 0) AS media_count,
			stats.last_message_at,
			COALESCE(last_message.preview, '') AS preview
		FROM conversations c
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) AS message_count,
				COUNT(*) FILTER (
					WHERE m.media_type IS NOT NULL
				) AS media_count,
				MAX(m.updated_at) AS last_message_at
			FROM messages m
			WHERE m.conversation_id = c.id
		) AS stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				CASE
					WHEN m.is_deleted THEN '[deleted]'
					WHEN m.text <> '' THEN LEFT(m.text, 80)
					WHEN m.caption <> '' THEN LEFT(m.caption, 80)
					WHEN m.media_type IS NOT NULL THEN '[' || m.media_type || ']'
					ELSE '[empty]'
				END AS preview
			FROM messages m
			WHERE m.conversation_id = c.id
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS last_message ON TRUE
		WHERE c.business_connection_id = $1
			AND (
				$2 = '%'
				OR LOWER(c.chat_title) LIKE $2
				OR LOWER(COALESCE(c.chat_username, '')) LIKE $2
				OR CAST(c.chat_id AS TEXT) LIKE REPLACE($2, '%', '')
			)
		ORDER BY stats.last_message_at DESC NULLS LAST, c.updated_at DESC
		LIMIT $3 OFFSET $4`,
		strings.TrimSpace(businessConnectionID),
		searchPattern,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConversationSummary
	for rows.Next() {
		var item ConversationSummary
		var messageCount int64
		var mediaCount int64

		if err := rows.Scan(
			&item.ID,
			&item.BusinessConnection,
			&item.ChatID,
			&item.ChatTitle,
			&item.ChatUsername,
			&messageCount,
			&mediaCount,
			&item.LastMessageAt,
			&item.LastPreview,
		); err != nil {
			return nil, err
		}

		item.MessageCount = int(messageCount)
		item.MediaCount = int(mediaCount)
		out = append(out, item)
	}

	return out, rows.Err()
}

func (ms *MessageStore) ListConversationsPaged(
	ctx context.Context,
	search string,
	limit int,
	offset int,
) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	searchPattern := "%"
	if trimmed := strings.TrimSpace(search); trimmed != "" {
		searchPattern = "%" + strings.ToLower(trimmed) + "%"
	}

	rows, err := ms.db.Query(
		ctx,
		`SELECT
			c.id,
			c.business_connection_id,
			c.chat_id,
			c.chat_title,
			COALESCE(c.chat_username, ''),
			COALESCE(stats.message_count, 0) AS message_count,
			COALESCE(stats.media_count, 0) AS media_count,
			stats.last_message_at,
			COALESCE(last_message.preview, '') AS preview
		FROM conversations c
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) AS message_count,
				COUNT(*) FILTER (
					WHERE m.media_type IS NOT NULL
				) AS media_count,
				MAX(m.updated_at) AS last_message_at
			FROM messages m
			WHERE m.conversation_id = c.id
		) AS stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				CASE
					WHEN m.is_deleted THEN '[deleted]'
					WHEN m.text <> '' THEN LEFT(m.text, 80)
					WHEN m.caption <> '' THEN LEFT(m.caption, 80)
					WHEN m.media_type IS NOT NULL THEN '[' || m.media_type || ']'
					ELSE '[empty]'
				END AS preview
			FROM messages m
			WHERE m.conversation_id = c.id
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS last_message ON TRUE
		WHERE (
			$1 = '%'
			OR LOWER(c.chat_title) LIKE $1
			OR LOWER(COALESCE(c.chat_username, '')) LIKE $1
			OR CAST(c.chat_id AS TEXT) LIKE REPLACE($1, '%', '')
		)
		ORDER BY stats.last_message_at DESC NULLS LAST, c.updated_at DESC
		LIMIT $2 OFFSET $3`,
		searchPattern,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConversationSummary
	for rows.Next() {
		var item ConversationSummary
		var messageCount int64
		var mediaCount int64

		if err := rows.Scan(
			&item.ID,
			&item.BusinessConnection,
			&item.ChatID,
			&item.ChatTitle,
			&item.ChatUsername,
			&messageCount,
			&mediaCount,
			&item.LastMessageAt,
			&item.LastPreview,
		); err != nil {
			return nil, err
		}

		item.MessageCount = int(messageCount)
		item.MediaCount = int(mediaCount)
		out = append(out, item)
	}

	return out, rows.Err()
}

func (ms *MessageStore) ConversationByID(ctx context.Context, conversationID int64) (ConversationSummary, bool, error) {
	row := ms.db.QueryRow(
		ctx,
		`SELECT
			c.id,
			c.business_connection_id,
			c.chat_id,
			c.chat_title,
			COALESCE(c.chat_username, ''),
			COALESCE(stats.message_count, 0) AS message_count,
			COALESCE(stats.media_count, 0) AS media_count,
			stats.last_message_at,
			COALESCE(last_message.preview, '') AS preview
		FROM conversations c
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) AS message_count,
				COUNT(*) FILTER (
					WHERE m.media_type IS NOT NULL
				) AS media_count,
				MAX(m.updated_at) AS last_message_at
			FROM messages m
			WHERE m.conversation_id = c.id
		) AS stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				CASE
					WHEN m.is_deleted THEN '[deleted]'
					WHEN m.text <> '' THEN LEFT(m.text, 80)
					WHEN m.caption <> '' THEN LEFT(m.caption, 80)
					WHEN m.media_type IS NOT NULL THEN '[' || m.media_type || ']'
					ELSE '[empty]'
				END AS preview
			FROM messages m
			WHERE m.conversation_id = c.id
			ORDER BY m.updated_at DESC, m.id DESC
			LIMIT 1
		) AS last_message ON TRUE
		WHERE c.id = $1`,
		conversationID,
	)

	var item ConversationSummary
	var messageCount int64
	var mediaCount int64

	err := row.Scan(
		&item.ID,
		&item.BusinessConnection,
		&item.ChatID,
		&item.ChatTitle,
		&item.ChatUsername,
		&messageCount,
		&mediaCount,
		&item.LastMessageAt,
		&item.LastPreview,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConversationSummary{}, false, nil
		}
		return ConversationSummary{}, false, err
	}

	item.MessageCount = int(messageCount)
	item.MediaCount = int(mediaCount)
	return item, true, nil
}

func (ms *MessageStore) HistoryByConversation(ctx context.Context, conversationID int64, limit int) ([]StoredMessage, error) {
	return ms.HistoryByConversationPage(ctx, conversationID, limit, 0)
}

func (ms *MessageStore) HistoryByConversationPage(
	ctx context.Context,
	conversationID int64,
	limit int,
	offset int,
) ([]StoredMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := ms.db.Query(
		ctx,
		`SELECT
			conversation_id,
			business_connection_id,
			chat_id,
			COALESCE((SELECT chat_title FROM conversations WHERE id = messages.conversation_id), ''),
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			NULL::bytea AS media_bytes,
			reply_to_message_id,
			backed_up,
			is_deleted,
			message_date,
			first_seen_at,
			updated_at,
			edited_at,
			deleted_at
		FROM (
			SELECT *
			FROM messages
			WHERE conversation_id = $1
			ORDER BY message_date DESC, id DESC
			LIMIT $2 OFFSET $3
		) AS messages
		ORDER BY message_date ASC, id ASC`,
		conversationID,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StoredMessage
	for rows.Next() {
		msg, err := scanStoredMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}

	return out, rows.Err()
}

func (ms *MessageStore) GetConversationMedia(
	ctx context.Context,
	conversationID int64,
	messageID int,
) (StoredMessage, bool, error) {
	row := ms.db.QueryRow(
		ctx,
		`SELECT
			conversation_id,
			business_connection_id,
			chat_id,
			COALESCE((SELECT chat_title FROM conversations WHERE id = messages.conversation_id), ''),
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			media_bytes,
			reply_to_message_id,
			backed_up,
			is_deleted,
			message_date,
			first_seen_at,
			updated_at,
			edited_at,
			deleted_at
		FROM messages
		WHERE conversation_id = $1
			AND message_id = $2
			AND media_type IS NOT NULL
		LIMIT 1`,
		conversationID,
		messageID,
	)

	msg, err := scanStoredMessage(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredMessage{}, false, nil
		}
		return StoredMessage{}, false, err
	}

	return msg, true, nil
}

func (ms *MessageStore) RevisionsByConversation(
	ctx context.Context,
	conversationID int64,
) (map[int][]MessageRevision, error) {
	rows, err := ms.db.Query(
		ctx,
		`SELECT
			message_id,
			event_type,
			text,
			caption,
			created_at
		FROM message_events
		WHERE conversation_id = $1
			AND event_type IN ('created', 'edited')
		ORDER BY message_id ASC, created_at ASC, id ASC`,
		conversationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int][]MessageRevision)
	for rows.Next() {
		var item MessageRevision
		if err := rows.Scan(
			&item.MessageID,
			&item.EventType,
			&item.Text,
			&item.Caption,
			&item.OccurredAt,
		); err != nil {
			return nil, err
		}
		out[item.MessageID] = append(out[item.MessageID], item)
	}

	return out, rows.Err()
}

func (ms *MessageStore) UpdateMediaPayload(
	ctx context.Context,
	businessConnectionID string,
	chatID int64,
	messageID int,
	filename string,
	mimeType string,
	data []byte,
) (bool, error) {
	if len(data) == 0 {
		return false, nil
	}

	tag, err := ms.db.Exec(
		ctx,
		`UPDATE messages
		SET
			media_bytes = $4,
			media_filename = COALESCE(NULLIF($5, ''), media_filename),
			media_mime = COALESCE(NULLIF($6, ''), media_mime),
			updated_at = NOW()
		WHERE business_connection_id = $1
			AND chat_id = $2
			AND message_id = $3
			AND media_type IS NOT NULL`,
		businessConnectionID,
		chatID,
		messageID,
		data,
		filename,
		mimeType,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (ms *MessageStore) UpdateConversationMediaPayload(
	ctx context.Context,
	conversationID int64,
	messageID int,
	filename string,
	mimeType string,
	data []byte,
) (bool, error) {
	if len(data) == 0 {
		return false, nil
	}

	tag, err := ms.db.Exec(
		ctx,
		`UPDATE messages
		SET
			media_bytes = $3,
			media_filename = COALESCE(NULLIF($4, ''), media_filename),
			media_mime = COALESCE(NULLIF($5, ''), media_mime),
			updated_at = NOW()
		WHERE conversation_id = $1
			AND message_id = $2
			AND media_type IS NOT NULL`,
		conversationID,
		messageID,
		data,
		filename,
		mimeType,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (ms *MessageStore) PendingMediaWithoutBytes(
	ctx context.Context,
	limit int,
	lookback time.Duration,
) ([]StoredMessage, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 500 {
		limit = 500
	}
	if lookback <= 0 {
		lookback = 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-lookback)

	rows, err := ms.db.Query(
		ctx,
		`SELECT
			conversation_id,
			business_connection_id,
			chat_id,
			COALESCE((SELECT chat_title FROM conversations WHERE id = messages.conversation_id), ''),
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			media_bytes,
			reply_to_message_id,
			backed_up,
			is_deleted,
			message_date,
			first_seen_at,
			updated_at,
			edited_at,
			deleted_at
		FROM messages
		WHERE media_type IS NOT NULL
			AND media_file_id IS NOT NULL
			AND (media_bytes IS NULL OR OCTET_LENGTH(media_bytes) = 0)
			AND first_seen_at >= $2
		ORDER BY updated_at DESC, id DESC
		LIMIT $1`,
		limit,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]StoredMessage, 0, limit)
	for rows.Next() {
		msg, err := scanStoredMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}

	return out, rows.Err()
}

func (ms *MessageStore) MediaByConversation(ctx context.Context, conversationID int64, limit int) ([]StoredMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	rows, err := ms.db.Query(
		ctx,
		`SELECT
			conversation_id,
			business_connection_id,
			chat_id,
			COALESCE((SELECT chat_title FROM conversations WHERE id = messages.conversation_id), ''),
			message_id,
			from_user_id,
			from_username,
			from_name,
			is_owner,
			text,
			caption,
			media_type,
			media_file_id,
			media_filename,
			media_mime,
			media_bytes,
			reply_to_message_id,
			backed_up,
			is_deleted,
			message_date,
			first_seen_at,
			updated_at,
			edited_at,
			deleted_at
		FROM messages
		WHERE conversation_id = $1
			AND media_type IS NOT NULL
			AND is_deleted = FALSE
		ORDER BY message_date DESC, id DESC
		LIMIT $2`,
		conversationID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StoredMessage
	for rows.Next() {
		msg, err := scanStoredMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}

	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanStoredMessage(row rowScanner) (StoredMessage, error) {
	var out StoredMessage
	var fromUserID *int64
	var fromUsername *string
	var fromName *string
	var mediaType *string
	var mediaFileID *string
	var mediaFilename *string
	var mediaMIME *string
	var replyToMessageID *int
	var editedAt *time.Time
	var deletedAt *time.Time

	err := row.Scan(
		&out.ConversationID,
		&out.BusinessConnectionID,
		&out.ChatID,
		&out.ChatTitle,
		&out.MessageID,
		&fromUserID,
		&fromUsername,
		&fromName,
		&out.IsOwner,
		&out.Text,
		&out.Caption,
		&mediaType,
		&mediaFileID,
		&mediaFilename,
		&mediaMIME,
		&out.MediaBytes,
		&replyToMessageID,
		&out.BackedUp,
		&out.IsDeleted,
		&out.MessageDate,
		&out.FirstSeenAt,
		&out.UpdatedAt,
		&editedAt,
		&deletedAt,
	)
	if err != nil {
		return StoredMessage{}, err
	}

	if fromUserID != nil {
		out.FromUserID = *fromUserID
	}
	if fromUsername != nil {
		out.FromUsername = *fromUsername
	}
	if fromName != nil {
		out.FromName = *fromName
	}
	if mediaType != nil {
		out.MediaType = *mediaType
	}
	if mediaFileID != nil {
		out.MediaFileID = *mediaFileID
	}
	if mediaFilename != nil {
		out.MediaFilename = *mediaFilename
	}
	if mediaMIME != nil {
		out.MediaMIME = *mediaMIME
	}
	if replyToMessageID != nil {
		out.ReplyToMessageID = *replyToMessageID
	}
	out.EditedAt = editedAt
	out.DeletedAt = deletedAt

	return out, nil
}

func nullString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return v
}
