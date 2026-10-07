package store

import (
	"context"
	"fmt"
)

// ProjectRoles is the repository for users' roles in particular projects, which replace
// their global role there.
type ProjectRoles struct{ db querier }

// ProjectRole is one user's role in one project.
type ProjectRole struct {
	UserID    string
	ProjectID string
	Role      string
}

// ByUser returns a user's project roles by project id. A branch environment has no
// roles of its own: it gets the user's role in its parent, so a role given later or a
// new team member reaches the environments that already exist.
func (r *ProjectRoles) ByUser(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.id, r.role FROM project_roles r
		JOIN projects top ON top.id = r.project_id AND top.parent_id = ''
		JOIN projects p ON p.id = top.id OR p.parent_id = top.id
		WHERE r.user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("select project roles: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var project, role string
		if err := rows.Scan(&project, &role); err != nil {
			return nil, err
		}
		out[project] = role
	}
	return out, rows.Err()
}

// ByProject returns the users with a role of their own in a project.
func (r *ProjectRoles) ByProject(ctx context.Context, projectID string) ([]ProjectRole, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT user_id, project_id, role FROM project_roles WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("select project roles: %w", err)
	}
	defer rows.Close()
	var out []ProjectRole
	for rows.Next() {
		var pr ProjectRole
		if err := rows.Scan(&pr.UserID, &pr.ProjectID, &pr.Role); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// Set gives a user a role in a project; an empty role removes it, so the global role
// applies there again.
func (r *ProjectRoles) Set(ctx context.Context, userID, projectID, role string) error {
	if role == "" {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM project_roles WHERE user_id = ? AND project_id = ?`, userID, projectID); err != nil {
			return fmt.Errorf("delete project role: %w", err)
		}
		return nil
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_roles (user_id, project_id, role) VALUES (?, ?, ?)
		ON CONFLICT (user_id, project_id) DO UPDATE SET role = excluded.role`, userID, projectID, role)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("project role: %w", ErrNotFound)
		}
		return fmt.Errorf("set project role: %w", err)
	}
	return nil
}
