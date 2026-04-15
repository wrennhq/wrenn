package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// UserService provides user management operations.
type UserService struct {
	DB *db.Queries
}

// AdminUserRow is the shape returned by AdminListUsers.
type AdminUserRow struct {
	ID          pgtype.UUID
	Email       string
	Name        string
	IsAdmin     bool
	Status      string
	CreatedAt   time.Time
	TeamsJoined int32
	TeamsOwned  int32
}

// AdminListUsers returns a paginated list of all non-deleted users with team counts.
func (s *UserService) AdminListUsers(ctx context.Context, limit, offset int32) ([]AdminUserRow, int32, error) {
	users, err := s.DB.ListUsersAdmin(ctx, db.ListUsersAdminParams{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	total, err := s.DB.CountUsersAdmin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows := make([]AdminUserRow, len(users))
	for i, u := range users {
		rows[i] = AdminUserRow{
			ID:          u.ID,
			Email:       u.Email,
			Name:        u.Name,
			IsAdmin:     u.IsAdmin,
			Status:      u.Status,
			CreatedAt:   u.CreatedAt.Time,
			TeamsJoined: u.TeamsJoined,
			TeamsOwned:  u.TeamsOwned,
		}
	}
	return rows, total, nil
}

// SetUserStatus sets the status of a user account.
func (s *UserService) SetUserStatus(ctx context.Context, userID pgtype.UUID, status string) error {
	if err := s.DB.SetUserStatus(ctx, db.SetUserStatusParams{
		ID:     userID,
		Status: status,
	}); err != nil {
		return fmt.Errorf("set user status: %w", err)
	}
	return nil
}
