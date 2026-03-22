package service

import (
	"context"
	"fmt"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

// APIKeyService provides API key operations shared between the REST API and the dashboard.
type APIKeyService struct {
	DB *db.Queries
}

// APIKeyCreateResult holds the result of creating an API key, including the
// plaintext key which is only available at creation time.
type APIKeyCreateResult struct {
	Row       db.TeamApiKey
	Plaintext string
}

// Create generates a new API key for the given team.
func (s *APIKeyService) Create(ctx context.Context, teamID, userID, name string) (APIKeyCreateResult, error) {
	if name == "" {
		name = "Unnamed API Key"
	}

	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		return APIKeyCreateResult{}, fmt.Errorf("generate key: %w", err)
	}

	row, err := s.DB.InsertAPIKey(ctx, db.InsertAPIKeyParams{
		ID:        id.NewAPIKeyID(),
		TeamID:    teamID,
		Name:      name,
		KeyHash:   hash,
		KeyPrefix: auth.APIKeyPrefix(plaintext),
		CreatedBy: userID,
	})
	if err != nil {
		return APIKeyCreateResult{}, fmt.Errorf("insert key: %w", err)
	}

	return APIKeyCreateResult{Row: row, Plaintext: plaintext}, nil
}

// List returns all API keys belonging to the given team.
func (s *APIKeyService) List(ctx context.Context, teamID string) ([]db.TeamApiKey, error) {
	return s.DB.ListAPIKeysByTeam(ctx, teamID)
}

// ListWithCreator returns all API keys for the team, joined with the creator's email.
func (s *APIKeyService) ListWithCreator(ctx context.Context, teamID string) ([]db.ListAPIKeysByTeamWithCreatorRow, error) {
	return s.DB.ListAPIKeysByTeamWithCreator(ctx, teamID)
}

// Delete removes an API key by ID, scoped to the given team.
func (s *APIKeyService) Delete(ctx context.Context, keyID, teamID string) error {
	return s.DB.DeleteAPIKey(ctx, db.DeleteAPIKeyParams{ID: keyID, TeamID: teamID})
}
