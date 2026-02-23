# dialog-spy-bot

Telegram Business bot для архивации переписки:
- сохраняет сообщения, правки и удаления;
- сохраняет медиа (фото/видео/файлы), включая кейс с reply на self-destruct медиа;
- показывает досье в веб-интерфейсе (PostgreSQL).

## Что умеет

- Архив бизнес-чатов в Postgres:
  - исходные сообщения;
  - история редактирований;
  - удаления;
  - медиа и их метаданные.
- Веб-досье:
  - список пользователей (business connections);
  - список чатов по пользователю;
  - таймлайн сообщений с предыдущими версиями.
- Уведомления в ЛС бота:
  - о редактировании;
  - об удалении (включая попытку отправить удаленное медиа);
  - о сохранении медиа по reply.
- Авто-ретеншн фото-байтов в БД (`PHOTO_RETENTION_DAYS`).
- Фоновая догрузка медиа в БД (`MEDIA_BACKFILL_*`), чтобы медиа появлялись в вебе автоматически.

## Стек

- Go
- PostgreSQL
- Docker / docker-compose

## Быстрый старт (Docker)

1. Создай `.env` (пример ниже).
2. Запусти:

```bash
docker compose up --build -d
```

3. Веб-интерфейс:
- `http://localhost:8090`
- если задан `WEB_UI_TOKEN`, вход по ссылке:
  - `http://localhost:8090/?token=<WEB_UI_TOKEN>`

## Запуск без Docker

1. Подними PostgreSQL.
2. Заполни `.env`.
3. Запусти:

```bash
go run .
```

## Пример `.env`

```env
BOT_TOKEN=<telegram_bot_token>
YOUR_USER_ID=<your_telegram_user_id>

DATABASE_URL=postgres://spybot:spybot@localhost:5432/spybot?sslmode=disable

ADMIN_USER_IDS=1013161349

WEB_PUBLIC_URL=http://localhost:8090
WEB_UI_TOKEN=
WEB_ADDR=:8090

MEDIA_MAX_MB=50
PHOTO_RETENTION_DAYS=3

MEDIA_BACKFILL_BATCH=40
MEDIA_BACKFILL_INTERVAL_SEC=30
MEDIA_BACKFILL_LOOKBACK_HOURS=24
```

Примечание:
- `PORT` используется автоматически как fallback для `WEB_ADDR` (удобно для Railway).
- `MESSAGE_TTL_HOURS` больше не используется.

## Команды бота

Для обычного пользователя:
- `/start`

Для админов:
- `/help`
- `/stats`
- `/web`
- `/chats [limit]`
- `/history <conversation_id> [limit]`
- `/media <conversation_id> [limit]`

## Railway

1. `New Project` -> `Deploy from GitHub Repo`.
2. Добавь PostgreSQL service.
3. В переменные сервиса бота:
   - `BOT_TOKEN`
   - `YOUR_USER_ID`
   - `ADMIN_USER_IDS`
   - `DATABASE_URL` (из Railway Postgres)
   - `WEB_PUBLIC_URL` (домен Railway)
   - `WEB_UI_TOKEN` (опционально)
   - `MEDIA_MAX_MB`
   - `PHOTO_RETENTION_DAYS`
   - `MEDIA_BACKFILL_BATCH`
   - `MEDIA_BACKFILL_INTERVAL_SEC`
   - `MEDIA_BACKFILL_LOOKBACK_HOURS`

## Частые проблемы

- `Conflict: terminated by other getUpdates request`
  - одновременно запущено больше одного инстанса бота с одним токеном.
- Медиа не открылось в вебе
  - файл мог быть уже недоступен у Telegram;
  - проверь логи и параметры `MEDIA_BACKFILL_*`.

