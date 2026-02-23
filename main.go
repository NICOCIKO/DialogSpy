package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	InitBotStyleFromEnv()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("BOT_TOKEN is not set")
	}

	yourUserIDStr := os.Getenv("YOUR_USER_ID")
	if yourUserIDStr == "" {
		log.Fatal("YOUR_USER_ID is not set")
	}

	yourUserID, err := strconv.ParseInt(yourUserIDStr, 10, 64)
	if err != nil {
		log.Fatal("YOUR_USER_ID must be int64:", err)
	}
	accessControl := NewAccessControl(yourUserID, os.Getenv("ADMIN_USER_IDS"))

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	mediaMaxMB := 50
	if mediaMaxMBStr := os.Getenv("MEDIA_MAX_MB"); mediaMaxMBStr != "" {
		if parsed, err := strconv.Atoi(mediaMaxMBStr); err == nil && parsed > 0 {
			mediaMaxMB = parsed
		}
	}
	mediaMaxBytes := int64(mediaMaxMB) << 20

	mediaBackfillBatch := 40
	if mediaBackfillBatchStr := os.Getenv("MEDIA_BACKFILL_BATCH"); mediaBackfillBatchStr != "" {
		if parsed, err := strconv.Atoi(mediaBackfillBatchStr); err == nil && parsed > 0 {
			mediaBackfillBatch = parsed
		}
	}
	mediaBackfillIntervalSec := 30
	if mediaBackfillIntervalStr := os.Getenv("MEDIA_BACKFILL_INTERVAL_SEC"); mediaBackfillIntervalStr != "" {
		if parsed, err := strconv.Atoi(mediaBackfillIntervalStr); err == nil && parsed > 0 {
			mediaBackfillIntervalSec = parsed
		}
	}
	mediaBackfillLookbackHours := 24
	if mediaBackfillLookbackStr := os.Getenv("MEDIA_BACKFILL_LOOKBACK_HOURS"); mediaBackfillLookbackStr != "" {
		if parsed, err := strconv.Atoi(mediaBackfillLookbackStr); err == nil && parsed > 0 {
			mediaBackfillLookbackHours = parsed
		}
	}

	photoRetentionDays := 3
	if photoRetentionDaysStr := os.Getenv("PHOTO_RETENTION_DAYS"); photoRetentionDaysStr != "" {
		if parsed, err := strconv.Atoi(photoRetentionDaysStr); err == nil && parsed > 0 {
			photoRetentionDays = parsed
		}
	}

	webAddr := os.Getenv("WEB_ADDR")
	if strings.TrimSpace(webAddr) == "" {
		if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
			webAddr = ":" + port
		} else {
			webAddr = ":8090"
		}
	}
	webToken := strings.TrimSpace(os.Getenv("WEB_UI_TOKEN"))
	webPublicURL := strings.TrimSpace(os.Getenv("WEB_PUBLIC_URL"))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	store, err := NewMessageStore(ctx, databaseURL)
	if err != nil {
		log.Fatalf("failed to init message store: %v", err)
	}
	defer store.Close()

	if updated, err := store.RecalculateOwnerFlags(ctx); err != nil {
		log.Printf("owner flags recalculation failed: %v", err)
	} else if updated > 0 {
		log.Printf("owner flags recalculated: %d message(s) updated", updated)
	}

	startPhotoRetentionWorker(ctx, store, photoRetentionDays, time.Hour)

	opts := []bot.Option{
		bot.WithAllowedUpdates(bot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateBusinessConnection,
			models.AllowedUpdateBusinessMessage,
			models.AllowedUpdateEditedBusinessMessage,
			models.AllowedUpdateDeletedBusinessMessages,
		}),
		bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, update *models.Update) {
			handleUpdate(ctx, b, update, store, accessControl, mediaMaxBytes, webPublicURL, webToken)
		}),
	}

	b, err := bot.New(botToken, opts...)
	if err != nil {
		log.Fatalf("failed to init bot: %v", err)
	}

	webServer := NewWebServer(store, b, webAddr, webToken, mediaMaxBytes)
	startMediaBackfillWorker(
		ctx,
		store,
		b,
		mediaMaxBytes,
		time.Duration(mediaBackfillIntervalSec)*time.Second,
		mediaBackfillBatch,
		time.Duration(mediaBackfillLookbackHours)*time.Hour,
	)
	go func() {
		if err := webServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web server stopped: %v", err)
			cancel()
		}
	}()

	if webPublicURL != "" {
		log.Printf("web ui: %s", webPublicURL)
	}

	b.Start(ctx)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = webServer.Shutdown(shutdownCtx)
}

func startPhotoRetentionWorker(
	ctx context.Context,
	store *MessageStore,
	retentionDays int,
	interval time.Duration,
) {
	if retentionDays <= 0 || interval <= 0 {
		return
	}

	runCleanup := func() {
		cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
		updated, err := store.PurgePhotoBytesOlderThan(ctx, cutoff)
		if err != nil {
			log.Printf("photo retention cleanup failed: %v", err)
			return
		}
		if updated > 0 {
			log.Printf("photo retention cleanup: purged %d photo payload(s) older than %d day(s)", updated, retentionDays)
		}
	}

	runCleanup()

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCleanup()
			}
		}
	}()
}

func startMediaBackfillWorker(
	ctx context.Context,
	store *MessageStore,
	b *bot.Bot,
	maxMediaBytes int64,
	interval time.Duration,
	batch int,
	lookback time.Duration,
) {
	if store == nil || b == nil || maxMediaBytes <= 0 || interval <= 0 || batch <= 0 || lookback <= 0 {
		return
	}

	runBackfill := func() {
		pending, err := store.PendingMediaWithoutBytes(ctx, batch, lookback)
		if err != nil {
			log.Printf("media backfill query failed: %v", err)
			return
		}
		if len(pending) == 0 {
			return
		}

		updatedCount := 0
		for _, msg := range pending {
			if msg.MediaFileID == "" {
				continue
			}

			downloaded, err := downloadTelegramFileWithRetry(ctx, b, msg.MediaFileID, maxMediaBytes, 6, 300*time.Millisecond)
			if err != nil || len(downloaded.Data) == 0 {
				continue
			}

			updated, err := store.UpdateMediaPayload(
				ctx,
				msg.BusinessConnectionID,
				msg.ChatID,
				msg.MessageID,
				downloaded.Filename,
				downloaded.MIME,
				downloaded.Data,
			)
			if err != nil {
				log.Printf("media backfill persist failed for message %d: %v", msg.MessageID, err)
				continue
			}
			if updated {
				updatedCount++
			}
		}

		if updatedCount > 0 {
			log.Printf("media backfill: hydrated %d message(s)", updatedCount)
		}
	}

	runBackfill()

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runBackfill()
			}
		}
	}()
}
