package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Users beyond the first are invited: the admin names them and gives them a role,
// Envoryx hands back a link that is valid for InviteTTL, and the new user sets a password
// through it. The same link resets a forgotten password. Envoryx sends no mail; the admin
// passes the link on.

// InviteTTL is how long an invitation link stays valid.
const InviteTTL = 48 * time.Hour

var (
	// ErrInviteInvalid is returned for an unknown, used or expired invitation.
	ErrInviteInvalid = errors.New("this invitation link is invalid or has expired; ask an admin for a new one")
	// ErrLastAdmin keeps the instance from losing its last admin.
	ErrLastAdmin = errors.New("the last admin cannot be removed, disabled or given another role")
)

// newInvite returns a fresh invitation token and its hash.
func newInvite() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate invitation: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

// InviteUser creates a user without a password and returns the invitation token.
func (s *Service) InviteUser(ctx context.Context, username string, role Role) (string, store.User, error) {
	if err := validate.Username(username); err != nil {
		return "", store.User{}, err
	}
	if _, err := ParseRole(string(role)); err != nil {
		return "", store.User{}, fmt.Errorf("%w: %v", validate.ErrInvalid, err)
	}
	token, hash, err := newInvite()
	if err != nil {
		return "", store.User{}, err
	}
	u, err := s.store.Users.Insert(ctx, store.User{Username: username, Role: string(role), InviteHash: hash, InviteExpiresAt: s.now().Add(InviteTTL)})
	if err != nil {
		return "", store.User{}, err
	}
	return token, u, nil
}

// RenewInvite gives a user a new invitation, which also resets a forgotten password:
// the old one keeps working until the link is used.
func (s *Service) RenewInvite(ctx context.Context, id string) (string, store.User, error) {
	u, err := s.store.Users.ByID(ctx, id)
	if err != nil {
		return "", store.User{}, err
	}
	token, hash, err := newInvite()
	if err != nil {
		return "", store.User{}, err
	}
	u.InviteHash, u.InviteExpiresAt = hash, s.now().Add(InviteTTL)
	if err := s.store.Users.SetInvite(ctx, id, hash, u.InviteExpiresAt); err != nil {
		return "", store.User{}, err
	}
	return token, u, nil
}

// Invitation returns the user an invitation token belongs to, if it is still valid.
func (s *Service) Invitation(ctx context.Context, token string) (store.User, error) {
	if token == "" {
		return store.User{}, ErrInviteInvalid
	}
	u, err := s.store.Users.ByInvite(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrInviteInvalid
	}
	if err != nil {
		return store.User{}, err
	}
	if u.Disabled || s.now().After(u.InviteExpiresAt) {
		return store.User{}, ErrInviteInvalid
	}
	return u, nil
}

// AcceptInvite sets the password of an invited user, ends the invitation and signs the
// user's other sessions out (a reset must lock out whoever knew the old password).
func (s *Service) AcceptInvite(ctx context.Context, token, password string) (store.User, error) {
	u, err := s.Invitation(ctx, token)
	if err != nil {
		return store.User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return store.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	if err := s.store.Users.AcceptInvite(ctx, u.ID, u.InviteHash, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.User{}, ErrInviteInvalid
		}
		return store.User{}, err
	}
	if err := s.store.Sessions.DeleteByUser(ctx, u.ID); err != nil {
		return store.User{}, err
	}
	u.PasswordHash, u.InviteHash = hash, ""
	return u, nil
}

// keepsAnAdmin fails when changing the user would leave no active admin.
func (s *Service) keepsAnAdmin(ctx context.Context, u store.User) error {
	if Role(u.Role) != RoleAdmin || u.Disabled {
		return nil
	}
	n, err := s.store.Users.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// SetUserRole changes a user's global role.
func (s *Service) SetUserRole(ctx context.Context, id string, role Role) (store.User, error) {
	if _, err := ParseRole(string(role)); err != nil {
		return store.User{}, fmt.Errorf("%w: %v", validate.ErrInvalid, err)
	}
	u, err := s.store.Users.ByID(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	if role != RoleAdmin {
		if err := s.keepsAnAdmin(ctx, u); err != nil {
			return store.User{}, err
		}
	}
	if err := s.store.Users.SetRole(ctx, id, string(role)); err != nil {
		return store.User{}, err
	}
	u.Role = string(role)
	return u, nil
}

// SetUserDisabled disables a user (signing out their sessions) or enables them again.
func (s *Service) SetUserDisabled(ctx context.Context, id string, disabled bool) (store.User, error) {
	u, err := s.store.Users.ByID(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	if disabled {
		if err := s.keepsAnAdmin(ctx, u); err != nil {
			return store.User{}, err
		}
	}
	if err := s.store.Users.SetDisabled(ctx, id, disabled); err != nil {
		return store.User{}, err
	}
	if disabled {
		if err := s.store.Sessions.DeleteByUser(ctx, id); err != nil {
			return store.User{}, err
		}
	}
	u.Disabled = disabled
	return u, nil
}

// DeleteUser removes a user with their sessions, API tokens and project roles.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	u, err := s.store.Users.ByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.keepsAnAdmin(ctx, u); err != nil {
		return err
	}
	return s.store.Users.Delete(ctx, id)
}

// SetProjectRole gives a user a role in one project; an empty role removes it.
func (s *Service) SetProjectRole(ctx context.Context, userID, projectID string, role Role) error {
	if role != "" {
		if _, err := ParseRole(string(role)); err != nil {
			return fmt.Errorf("%w: %v", validate.ErrInvalid, err)
		}
	}
	if _, err := s.store.Users.ByID(ctx, userID); err != nil {
		return err
	}
	return s.store.Roles.Set(ctx, userID, projectID, string(role))
}
