package channels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/events"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
)

// Valid providers.
var validProviders = map[string]bool{
	"discord":    true,
	"slack":      true,
	"teams":      true,
	"googlechat": true,
	"telegram":   true,
	"matrix":     true,
	"webhook":    true,
}

// Required config fields per provider.
var requiredFields = map[string][]string{
	"discord":    {"webhook_url"},
	"slack":      {"webhook_url"},
	"teams":      {"webhook_url"},
	"googlechat": {"webhook_url"},
	"telegram":   {"bot_token", "chat_id"},
	"matrix":     {"homeserver_url", "access_token", "room_id"},
	"webhook":    {"url"},
}

// validEvents maps event type strings to true for validation.
var validEvents map[string]bool

func init() {
	validEvents = make(map[string]bool, len(events.SubscribableEventTypes))
	for _, et := range events.SubscribableEventTypes {
		validEvents[et] = true
	}
}

// Service handles channel CRUD operations.
type Service struct {
	DB     *db.Queries
	EncKey [32]byte
}

// CreateParams holds the parameters for creating a channel.
type CreateParams struct {
	TeamID   pgtype.UUID
	Name     string
	Provider string
	Config   map[string]string
	Events   []string
}

// CreateResult holds the result of creating a channel.
type CreateResult struct {
	Channel         db.Channel
	PlaintextSecret string // non-empty only for webhook provider
}

// Create creates a new notification channel.
func (s *Service) Create(ctx context.Context, p CreateParams) (CreateResult, error) {
	clean, err := cleanName(p.Name)
	if err != nil {
		return CreateResult{}, err
	}
	p.Name = clean

	if !validProviders[p.Provider] {
		return CreateResult{}, apperr.ValidationFailed.Msgf("Unsupported provider %q.", p.Provider).With("field", "provider")
	}

	if len(p.Events) == 0 {
		return CreateResult{}, apperr.ValidationFailed.Msg("At least one event type is required.").With("field", "events")
	}
	for _, et := range p.Events {
		if !validEvents[et] {
			return CreateResult{}, apperr.ValidationFailed.Msgf("Unknown event type %q.", et).With("field", "events")
		}
	}

	// Validate required config fields.
	for _, field := range requiredFields[p.Provider] {
		if p.Config[field] == "" {
			return CreateResult{}, apperr.ValidationFailed.Msgf("The %s field is required for %s.", field, p.Provider).With("field", field)
		}
	}

	// For webhooks, auto-generate secret if not provided.
	var plaintextSecret string
	if p.Provider == "webhook" {
		if p.Config["secret"] == "" {
			secret := generateSecret()
			p.Config["secret"] = secret
			plaintextSecret = secret
		} else {
			plaintextSecret = p.Config["secret"]
		}
	}

	// Encrypt config fields.
	encrypted := make(map[string]string, len(p.Config))
	for k, v := range p.Config {
		enc, err := EncryptSecret(s.EncKey, v)
		if err != nil {
			return CreateResult{}, fmt.Errorf("encrypt config field %s: %w", k, err)
		}
		encrypted[k] = enc
	}

	configJSON, err := json.Marshal(encrypted)
	if err != nil {
		return CreateResult{}, fmt.Errorf("marshal config: %w", err)
	}

	ch, err := s.DB.InsertChannel(ctx, db.InsertChannelParams{
		ID:         id.NewChannelID(),
		TeamID:     p.TeamID,
		Name:       p.Name,
		Provider:   p.Provider,
		Config:     configJSON,
		EventTypes: p.Events,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return CreateResult{}, apperr.Conflict.Msgf("A channel named %q already exists.", p.Name)
		}
		return CreateResult{}, fmt.Errorf("insert channel: %w", err)
	}

	return CreateResult{Channel: ch, PlaintextSecret: plaintextSecret}, nil
}

// List returns all channels belonging to the given team.
func (s *Service) List(ctx context.Context, teamID pgtype.UUID) ([]db.Channel, error) {
	return s.DB.ListChannelsByTeam(ctx, teamID)
}

// Get returns a single channel by ID, scoped to the given team.
func (s *Service) Get(ctx context.Context, channelID, teamID pgtype.UUID) (db.Channel, error) {
	return s.DB.GetChannelByTeam(ctx, db.GetChannelByTeamParams{ID: channelID, TeamID: teamID})
}

// Update updates a channel's name and event types.
func (s *Service) Update(ctx context.Context, channelID, teamID pgtype.UUID, name string, eventTypes []string) (db.Channel, error) {
	clean, err := cleanName(name)
	if err != nil {
		return db.Channel{}, err
	}
	name = clean

	if len(eventTypes) == 0 {
		return db.Channel{}, apperr.ValidationFailed.Msg("At least one event type is required.").With("field", "events")
	}
	for _, et := range eventTypes {
		if !validEvents[et] {
			return db.Channel{}, apperr.ValidationFailed.Msgf("Unknown event type %q.", et).With("field", "events")
		}
	}

	ch, err := s.DB.UpdateChannel(ctx, db.UpdateChannelParams{
		ID:         channelID,
		TeamID:     teamID,
		Name:       name,
		EventTypes: eventTypes,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Channel{}, apperr.NotFound.Msg("Channel not found.")
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return db.Channel{}, apperr.Conflict.Msgf("A channel named %q already exists.", name)
		}
		return db.Channel{}, fmt.Errorf("update channel: %w", err)
	}
	return ch, nil
}

// RotateConfig replaces a channel's config with new provider secrets.
func (s *Service) RotateConfig(ctx context.Context, channelID, teamID pgtype.UUID, config map[string]string) (db.Channel, error) {
	// Look up the existing channel to get its provider for validation.
	ch, err := s.DB.GetChannelByTeam(ctx, db.GetChannelByTeamParams{ID: channelID, TeamID: teamID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Channel{}, apperr.NotFound.Msg("Channel not found.")
		}
		return db.Channel{}, fmt.Errorf("get channel: %w", err)
	}

	// Validate required config fields for this provider.
	for _, field := range requiredFields[ch.Provider] {
		if config[field] == "" {
			return db.Channel{}, apperr.ValidationFailed.Msgf("The %s field is required for %s.", field, ch.Provider).With("field", field)
		}
	}

	// For webhooks, auto-generate secret if not provided.
	if ch.Provider == "webhook" && config["secret"] == "" {
		config["secret"] = generateSecret()
	}

	// Encrypt all config fields.
	encrypted := make(map[string]string, len(config))
	for k, v := range config {
		enc, err := EncryptSecret(s.EncKey, v)
		if err != nil {
			return db.Channel{}, fmt.Errorf("encrypt config field %s: %w", k, err)
		}
		encrypted[k] = enc
	}

	configJSON, err := json.Marshal(encrypted)
	if err != nil {
		return db.Channel{}, fmt.Errorf("marshal config: %w", err)
	}

	updated, err := s.DB.UpdateChannelConfig(ctx, db.UpdateChannelConfigParams{
		ID:     channelID,
		TeamID: teamID,
		Config: configJSON,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Channel{}, apperr.NotFound.Msg("Channel not found.")
		}
		return db.Channel{}, fmt.Errorf("update channel config: %w", err)
	}
	return updated, nil
}

// Test validates config and sends a test notification without persisting anything.
func (s *Service) Test(ctx context.Context, provider string, config map[string]string) error {
	if !validProviders[provider] {
		return apperr.ValidationFailed.Msgf("Unsupported provider %q.", provider).With("field", "provider")
	}

	for _, field := range requiredFields[provider] {
		if config[field] == "" {
			return apperr.ValidationFailed.Msgf("The %s field is required for %s.", field, provider).With("field", field)
		}
	}

	// For webhooks, auto-generate a temporary secret if not provided.
	if provider == "webhook" && config["secret"] == "" {
		config["secret"] = generateSecret()
	}

	testEvent := events.Event{
		Event:     "channel.test",
		Timestamp: events.Now(),
		TeamID:    "test",
		Actor:     events.Actor{Type: events.ActorSystem},
		Resource:  events.Resource{ID: "test", Type: "channel"},
	}

	return Deliver(ctx, provider, config, testEvent)
}

// Delete removes a channel by ID, scoped to the given team.
func (s *Service) Delete(ctx context.Context, channelID, teamID pgtype.UUID) error {
	return s.DB.DeleteChannelByTeam(ctx, db.DeleteChannelByTeamParams{ID: channelID, TeamID: teamID})
}

// cleanName normalises a channel name: trim whitespace, lowercase, replace
// spaces with hyphens, then validate against SafeName rules.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	if err := validate.SafeName(name); err != nil {
		return "", apperr.ValidationFailed.WrapMsg(err, "Invalid channel name.").With("field", "name")
	}
	return name, nil
}

func generateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
