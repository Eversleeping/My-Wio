package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

type ProjectRemote struct {
	ID                  string    `db:"id" json:"id"`
	ProjectID           string    `db:"project_id" json:"project_id"`
	Name                string    `db:"name" json:"name"`
	Mode                string    `db:"mode" json:"mode"`
	Provider            string    `db:"provider" json:"provider"`
	Namespace           string    `db:"namespace" json:"namespace"`
	Repository          string    `db:"repository" json:"repository"`
	Visibility          string    `db:"visibility" json:"visibility"`
	CredentialProfileID string    `db:"credential_profile_id" json:"credential_profile_id,omitempty"`
	FetchURL            string    `db:"fetch_url" json:"fetch_url"`
	PushURL             string    `db:"push_url" json:"push_url"`
	WebURL              string    `db:"web_url" json:"web_url"`
	Status              string    `db:"status" json:"status"`
	Error               string    `db:"error" json:"error,omitempty"`
	CreatedAt           time.Time `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time `db:"updated_at" json:"updated_at"`
}

type BlankProjectRemoteSpec struct {
	Mode                string
	Provider            string
	Namespace           string
	Repository          string
	Visibility          string
	CredentialProfileID string
	URL                 string
}

func (s *Store) ProjectRemotes(ctx context.Context, projectID string) ([]ProjectRemote, error) {
	remotes := make([]ProjectRemote, 0)
	err := s.DB.SelectContext(ctx, &remotes, s.Q(`SELECT id,project_id,name,mode,provider,namespace,repository,visibility,credential_profile_id,fetch_url,push_url,web_url,status,error,created_at,updated_at FROM project_remotes WHERE project_id=? ORDER BY name`), projectID)
	return remotes, err
}

func (s *Store) ProjectRemote(ctx context.Context, projectID, name string) (ProjectRemote, error) {
	var remote ProjectRemote
	err := s.DB.GetContext(ctx, &remote, s.Q(`SELECT id,project_id,name,mode,provider,namespace,repository,visibility,credential_profile_id,fetch_url,push_url,web_url,status,error,created_at,updated_at FROM project_remotes WHERE project_id=? AND name=?`), projectID, name)
	return remote, err
}

func (s *Store) ensureProjectRemote(ctx context.Context, database sqlx.ExtContext, projectID, remoteURL string) error {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return nil
	}
	now := time.Now().UTC()
	_, err := database.ExecContext(ctx, s.Q(`INSERT INTO project_remotes(id,project_id,name,mode,fetch_url,push_url,status,created_at,updated_at) VALUES(?,?,'origin','existing',?,?,'ready',?,?) ON CONFLICT(project_id,name) DO NOTHING`), NewID(), projectID, remoteURL, remoteURL, now, now)
	return err
}

func (s *Store) prepareProjectRemote(ctx context.Context, tx sqlx.ExtContext, projectID string, spec BlankProjectRemoteSpec, status string) error {
	if strings.TrimSpace(spec.Mode) == "none" || strings.TrimSpace(spec.Mode) == "" {
		return nil
	}
	if status == "" {
		status = "ready"
	}
	_, err := tx.ExecContext(ctx, s.Q(`INSERT INTO project_remotes(id,project_id,name,mode,provider,namespace,repository,visibility,credential_profile_id,fetch_url,push_url,web_url,status,error,created_at,updated_at) VALUES(?,?, 'origin', ?,?,?,?,?,?,?,?,?,?,?,?,?)`), NewID(), projectID, spec.Mode, spec.Provider, spec.Namespace, spec.Repository, spec.Visibility, spec.CredentialProfileID, spec.URL, spec.URL, "", status, "", time.Now().UTC(), time.Now().UTC())
	return err
}

func (s *Store) UpdateProjectRemote(ctx context.Context, projectID string, result protocol.ProjectRemoteResult) error {
	if strings.TrimSpace(result.FetchURL) == "" || strings.TrimSpace(result.PushURL) == "" {
		return errors.New("project remote URLs are required")
	}
	dbResult, err := s.DB.ExecContext(ctx, s.Q(`UPDATE project_remotes SET provider=?,namespace=?,repository=?,fetch_url=?,push_url=?,web_url=?,status='ready',error='',updated_at=? WHERE project_id=? AND name='origin'`), result.Provider, result.Namespace, result.Repository, result.FetchURL, result.PushURL, result.WebURL, time.Now().UTC(), projectID)
	if err != nil {
		return err
	}
	rows, err := dbResult.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("project remote was not found")
	}
	return nil
}

func (s *Store) markProjectRemoteError(ctx context.Context, projectID, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE project_remotes SET status='failed',error=?,updated_at=? WHERE project_id=? AND name='origin'`), message, time.Now().UTC(), projectID)
	return err
}

func (s *Store) ProjectRemoteResult(ctx context.Context, projectID string) (protocol.ProjectRemoteResult, error) {
	remote, err := s.ProjectRemote(ctx, projectID, "origin")
	if err != nil {
		return protocol.ProjectRemoteResult{}, err
	}
	return protocol.ProjectRemoteResult{Provider: remote.Provider, Namespace: remote.Namespace, Repository: remote.Repository, FetchURL: remote.FetchURL, PushURL: remote.PushURL, WebURL: remote.WebURL}, nil
}

func operationCommand(operation Operation) (protocol.GitProjectCreateCommand, error) {
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return command, err
	}
	return command, nil
}

var errRemoteOperationState = errors.New("blank project remote operation is not preparing")
