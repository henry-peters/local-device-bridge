package telegram

import (
	"errors"
	"strings"
	"testing"
)

func TestAuthorizationUsesUserAndChatAllowlists(t *testing.T) {
	bot := New("token", []string{"user-1", "chat-2"}, nil, nil)
	if !bot.authorized("user-1", "other") {
		t.Fatal("allowlisted user was rejected")
	}
	if !bot.authorized("other", "chat-2") {
		t.Fatal("allowlisted chat was rejected")
	}
	if bot.authorized("other", "unknown") {
		t.Fatal("unknown Telegram identity was authorized")
	}
}

func TestPublicAuthorizationIsExplicit(t *testing.T) {
	bot := New("token", nil, nil, nil)
	if bot.authorized("user", "chat") {
		t.Fatal("public access was enabled by default")
	}
	bot.SetAllowPublic(true)
	if !bot.authorized("user", "chat") {
		t.Fatal("explicit public access was not honored")
	}
}

func TestTelegramNetworkErrorsRedactBotToken(t *testing.T) {
	bot := New("secret-bot-token", nil, nil, nil)
	err := bot.safeNetworkError(errors.New("GET https://api.telegram.org/botsecret-bot-token/getUpdates: connection refused"))
	if strings.Contains(err.Error(), "secret-bot-token") {
		t.Fatalf("network error leaked bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("network error was not marked redacted: %v", err)
	}
}
