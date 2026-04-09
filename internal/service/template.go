package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/db"
)

// TemplateService provides template/snapshot operations shared between the
// REST API and the dashboard.
type TemplateService struct {
	DB *db.Queries
}

// List returns all templates belonging to the given team. If typeFilter is
// non-empty, only templates of that type ("base" or "snapshot") are returned.
func (s *TemplateService) List(ctx context.Context, teamID pgtype.UUID, typeFilter string) ([]db.Template, error) {
	if typeFilter != "" {
		return s.DB.ListTemplatesByTeamAndType(ctx, db.ListTemplatesByTeamAndTypeParams{
			TeamID: teamID,
			Type:   typeFilter,
		})
	}
	return s.DB.ListTemplatesByTeam(ctx, teamID)
}
