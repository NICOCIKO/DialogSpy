package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
)

type DownloadedTelegramFile struct {
	Filename string
	MIME     string
	Data     []byte
}

func downloadTelegramFile(
	ctx context.Context,
	b *bot.Bot,
	fileID string,
	maxBytes int64,
) (DownloadedTelegramFile, error) {
	if strings.TrimSpace(fileID) == "" {
		return DownloadedTelegramFile{}, fmt.Errorf("empty file id")
	}
	if maxBytes <= 0 {
		return DownloadedTelegramFile{}, fmt.Errorf("maxBytes must be positive")
	}

	f, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return DownloadedTelegramFile{}, fmt.Errorf("getFile failed: %w", err)
	}
	if f.FilePath == "" {
		return DownloadedTelegramFile{}, fmt.Errorf("telegram returned empty file_path")
	}

	downloadURL := b.FileDownloadLink(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return DownloadedTelegramFile{}, fmt.Errorf("create download request failed: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DownloadedTelegramFile{}, fmt.Errorf("download media failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DownloadedTelegramFile{}, fmt.Errorf("download media bad status: %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return DownloadedTelegramFile{}, fmt.Errorf("read media failed: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return DownloadedTelegramFile{}, fmt.Errorf("media too large: %d bytes", len(data))
	}

	filename := path.Base(f.FilePath)
	if filename == "." || filename == "/" || filename == "" {
		filename = "backup_media"
	}

	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	}
	if mimeType == "" {
		mimeType = resp.Header.Get("Content-Type")
	}

	return DownloadedTelegramFile{
		Filename: filename,
		MIME:     mimeType,
		Data:     data,
	}, nil
}

func downloadTelegramFileWithRetry(
	ctx context.Context,
	b *bot.Bot,
	fileID string,
	maxBytes int64,
	attempts int,
	delay time.Duration,
) (DownloadedTelegramFile, error) {
	if attempts <= 1 {
		return downloadTelegramFile(ctx, b, fileID, maxBytes)
	}
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		file, err := downloadTelegramFile(ctx, b, fileID, maxBytes)
		if err == nil {
			return file, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return DownloadedTelegramFile{}, ctx.Err()
		}

		if i == attempts-1 {
			break
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return DownloadedTelegramFile{}, ctx.Err()
		case <-timer.C:
		}
		delay = delay * 2
	}

	return DownloadedTelegramFile{}, lastErr
}
