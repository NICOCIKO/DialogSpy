# docker build -t spy-bot .
# docker run -d --env-file .env --name spy-bot spy-bot

FROM golang:1.25.5-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o app

FROM alpine:3.19

WORKDIR /app
COPY --from=builder /app/app .

ENV TZ=UTC

CMD ["./app"]
