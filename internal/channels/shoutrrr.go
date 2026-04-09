package channels

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ShoutrrrURL builds a shoutrrr-compatible URL from structured provider config.
func ShoutrrrURL(provider string, config map[string]string) (string, error) {
	switch provider {
	case "discord":
		return discordURL(config)
	case "slack":
		return slackURL(config)
	case "teams":
		return teamsURL(config)
	case "googlechat":
		return googlechatURL(config)
	case "telegram":
		return telegramURL(config)
	case "matrix":
		return matrixURL(config)
	default:
		return "", fmt.Errorf("unsupported shoutrrr provider: %s", provider)
	}
}

// discordURL converts https://discord.com/api/webhooks/{id}/{token} → discord://{token}@{id}
func discordURL(config map[string]string) (string, error) {
	u, err := url.Parse(config["webhook_url"])
	if err != nil {
		return "", fmt.Errorf("invalid discord webhook URL: %w", err)
	}
	// Path: /api/webhooks/{id}/{token}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "webhooks" {
		return "", fmt.Errorf("unexpected discord webhook URL format")
	}
	webhookID, token := parts[2], parts[3]
	return fmt.Sprintf("discord://%s@%s?splitLines=No", token, webhookID), nil
}

// slackURL converts https://hooks.slack.com/services/T.../B.../XXX → slack://T.../B.../XXX
func slackURL(config map[string]string) (string, error) {
	u, err := url.Parse(config["webhook_url"])
	if err != nil {
		return "", fmt.Errorf("invalid slack webhook URL: %w", err)
	}
	// Path: /services/TXXXXX/BXXXXX/XXXXXXXX
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "services" {
		return "", fmt.Errorf("unexpected slack webhook URL format")
	}
	return fmt.Sprintf("slack://hook:%s-%s-%s@webhook", parts[1], parts[2], parts[3]), nil
}

// teamsWebhookRe extracts the 4 components from a Teams webhook URL.
// Format: https://<host>/<path>/{group}@{tenant}/IncomingWebhook/{altID}/{groupOwner}
var teamsWebhookRe = regexp.MustCompile(`([0-9a-f-]{36})@([0-9a-f-]{36})/[^/]+/([0-9a-f]{32})/([0-9a-f-]{36})`)

// teamsURL converts a Teams webhook URL → teams://Group@Tenant/AltID/GroupOwner
func teamsURL(config map[string]string) (string, error) {
	webhookURL := config["webhook_url"]
	if webhookURL == "" {
		return "", fmt.Errorf("teams webhook_url is required")
	}
	groups := teamsWebhookRe.FindStringSubmatch(webhookURL)
	if len(groups) != 5 {
		return "", fmt.Errorf("unexpected teams webhook URL format")
	}
	group, tenant, altID, groupOwner := groups[1], groups[2], groups[3], groups[4]
	return fmt.Sprintf("teams://%s@%s/%s/%s", group, tenant, altID, groupOwner), nil
}

// googlechatURL converts a Google Chat webhook URL to shoutrrr format.
// Input:  https://chat.googleapis.com/v1/spaces/SPACE/messages?key=KEY&token=TOKEN
// Output: googlechat://chat.googleapis.com/v1/spaces/SPACE/messages?key=KEY&token=TOKEN
func googlechatURL(config map[string]string) (string, error) {
	webhookURL := config["webhook_url"]
	if webhookURL == "" {
		return "", fmt.Errorf("googlechat webhook_url is required")
	}
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", fmt.Errorf("invalid googlechat webhook URL: %w", err)
	}
	if u.Host != "chat.googleapis.com" {
		return "", fmt.Errorf("unexpected googlechat webhook URL host: %s", u.Host)
	}
	// Rebuild as googlechat:// scheme with same host, path, and query.
	u.Scheme = "googlechat"
	return u.String(), nil
}

// telegramURL builds telegram://token@telegram/?chats=chatID
func telegramURL(config map[string]string) (string, error) {
	token := config["bot_token"]
	chatID := config["chat_id"]
	if token == "" || chatID == "" {
		return "", fmt.Errorf("telegram bot_token and chat_id are required")
	}
	return fmt.Sprintf("telegram://%s@telegram/?chats=%s", token, chatID), nil
}

// matrixURL builds matrix://user:token@homeserver/room
func matrixURL(config map[string]string) (string, error) {
	homeserver := config["homeserver_url"]
	token := config["access_token"]
	roomID := config["room_id"]
	if homeserver == "" || token == "" || roomID == "" {
		return "", fmt.Errorf("matrix homeserver_url, access_token, and room_id are required")
	}
	// Strip protocol from homeserver URL.
	host := strings.TrimPrefix(strings.TrimPrefix(homeserver, "https://"), "http://")
	// Room ID often starts with ! — URL-encode it.
	return fmt.Sprintf("matrix://:%s@%s/%s", url.PathEscape(token), host, url.PathEscape(roomID)), nil
}
