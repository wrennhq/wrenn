package service

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
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

// ListVisibleParams holds filters for the visible-templates listing.
type ListVisibleParams struct {
	TeamID     pgtype.UUID
	TypeFilter string // "" = any type
	Search     string // "" = no search
	Limit      int32
	Offset     int32
}

// ListVisible returns one page of templates the team may launch — its own,
// platform templates, and every public template across all teams — along with
// the total count for pagination. Rows carry the owning team's slug so the UI
// can render foreign public templates as "<slug>/<name>".
func (s *TemplateService) ListVisible(ctx context.Context, p ListVisibleParams) ([]db.ListVisibleTemplatesRow, int32, error) {
	rows, err := s.DB.ListVisibleTemplates(ctx, db.ListVisibleTemplatesParams{
		TeamID:     p.TeamID,
		TypeFilter: p.TypeFilter,
		Search:     p.Search,
		RowLimit:   p.Limit,
		RowOffset:  p.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.DB.CountVisibleTemplates(ctx, db.CountVisibleTemplatesParams{
		TeamID:     p.TeamID,
		TypeFilter: p.TypeFilter,
		Search:     p.Search,
	})
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// SetVisibility publishes or unpublishes a template the team owns. It returns
// TemplateNotFound if the team owns no template of that name.
func (s *TemplateService) SetVisibility(ctx context.Context, teamID pgtype.UUID, name string, public bool) (db.Template, error) {
	tmpl, err := s.DB.SetTemplateVisibility(ctx, db.SetTemplateVisibilityParams{
		TeamID:   teamID,
		Name:     name,
		IsPublic: public,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return db.Template{}, apperr.TemplateNotFound.Msgf("Your team does not own a template named %q.", name)
		}
		return db.Template{}, apperr.Internal.Wrap(err)
	}
	return tmpl, nil
}
