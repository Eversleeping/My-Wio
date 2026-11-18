// Package taskservice exposes task commands through a consumer-owned repository.
package taskservice

import (
	"context"

	"github.com/wio-platform/wio/internal/task"
)

type Repository interface {
	TaskTeam(context.Context, string, string) ([]task.TeamRequest, error)
	TaskPlans(context.Context, string, string) ([]task.PlanVersion, error)
	TaskPlan(context.Context, string, string, string) (task.Plan, error)
	TaskReviews(context.Context, string, string) ([]task.Review, error)
	DecideTaskReview(context.Context, string, string, task.ReviewDecision) (task.Review, error)
	CaptureTaskArtifact(context.Context, string, string, task.CaptureArtifact) (task.Artifact, error)
	HandoffTaskArtifact(context.Context, string, string, string, task.HandoffArtifact) (task.ArtifactHandoff, error)
	TaskArtifacts(context.Context, string, string) (task.Artifacts, error)
	CreateTask(context.Context, task.Create) (task.Task, error)
	UpdateTask(context.Context, string, string, task.Update) (task.Task, error)
	AppendTaskPlan(context.Context, string, string, task.AppendPlan) (task.PlanResult, error)
	TaskDetail(context.Context, string, string) (task.Detail, error)
	TaskPage(context.Context, task.Filter) (task.Page, error)
	TaskEvents(context.Context, string, string, int64, int) (task.EventPage, error)
	TaskRequest(context.Context, string, string, string) (task.Request, error)
	SetTaskGrant(context.Context, string, string, task.GrantInput) (task.GrantResult, error)
	StartTaskRun(context.Context, string, string, task.Start) (task.RunResult, error)
	ActOnTaskRun(context.Context, string, string, string, task.Action) (task.RunResult, error)
	AcceptTaskRun(context.Context, string, string, string, task.Acceptance) (task.Run, error)
	ArchiveTask(context.Context, string, string, task.Archive) (task.Task, error)
	TaskProject(context.Context, string) (string, error)
	RecentTasks(context.Context) ([]task.Recent, error)
	AdvanceTasks(context.Context, string) error
}
type Service struct{ Repository Repository }

func (s *Service) Create(ctx context.Context, in task.Create) (task.Task, error) {
	return s.Repository.CreateTask(ctx, in)
}
func (s *Service) Update(ctx context.Context, project, id string, in task.Update) (task.Task, error) {
	return s.Repository.UpdateTask(ctx, project, id, in)
}
func (s *Service) AppendPlan(ctx context.Context, project, id string, in task.AppendPlan) (task.PlanResult, error) {
	return s.Repository.AppendTaskPlan(ctx, project, id, in)
}
func (s *Service) Get(ctx context.Context, project, id string) (task.Detail, error) {
	return s.Repository.TaskDetail(ctx, project, id)
}
func (s *Service) List(ctx context.Context, f task.Filter) (task.Page, error) {
	return s.Repository.TaskPage(ctx, f)
}
func (s *Service) Events(ctx context.Context, project, id string, after int64, limit int) (task.EventPage, error) {
	return s.Repository.TaskEvents(ctx, project, id, after, limit)
}
func (s *Service) Request(ctx context.Context, actor, project, id string) (task.Request, error) {
	return s.Repository.TaskRequest(ctx, actor, project, id)
}
func (s *Service) SetGrant(ctx context.Context, project, id string, in task.GrantInput) (task.GrantResult, error) {
	return s.Repository.SetTaskGrant(ctx, project, id, in)
}
func (s *Service) Start(ctx context.Context, project, id string, in task.Start) (task.RunResult, error) {
	return s.Repository.StartTaskRun(ctx, project, id, in)
}
func (s *Service) Action(ctx context.Context, project, id, run string, in task.Action) (task.RunResult, error) {
	return s.Repository.ActOnTaskRun(ctx, project, id, run, in)
}
func (s *Service) Advance(ctx context.Context, owner string) error {
	return s.Repository.AdvanceTasks(ctx, owner)
}

func (s *Service) Accept(ctx context.Context, project, id, run string, in task.Acceptance) (task.Run, error) {
	return s.Repository.AcceptTaskRun(ctx, project, id, run, in)
}
func (s *Service) Archive(ctx context.Context, project, id string, in task.Archive) (task.Task, error) {
	return s.Repository.ArchiveTask(ctx, project, id, in)
}
func (s *Service) Project(ctx context.Context, id string) (string, error) {
	return s.Repository.TaskProject(ctx, id)
}
func (s *Service) Recent(ctx context.Context) ([]task.Recent, error) {
	return s.Repository.RecentTasks(ctx)
}

func (s *Service) CaptureArtifact(ctx context.Context, project, id string, in task.CaptureArtifact) (task.Artifact, error) {
	return s.Repository.CaptureTaskArtifact(ctx, project, id, in)
}
func (s *Service) HandoffArtifact(ctx context.Context, project, id, artifact string, in task.HandoffArtifact) (task.ArtifactHandoff, error) {
	return s.Repository.HandoffTaskArtifact(ctx, project, id, artifact, in)
}
func (s *Service) Artifacts(ctx context.Context, project, id string) (task.Artifacts, error) {
	return s.Repository.TaskArtifacts(ctx, project, id)
}

func (s *Service) Reviews(ctx context.Context, project, id string) ([]task.Review, error) {
	return s.Repository.TaskReviews(ctx, project, id)
}
func (s *Service) DecideReview(ctx context.Context, project, id string, in task.ReviewDecision) (task.Review, error) {
	return s.Repository.DecideTaskReview(ctx, project, id, in)
}

func (s *Service) Team(ctx context.Context, project, id string) ([]task.TeamRequest, error) {
	return s.Repository.TaskTeam(ctx, project, id)
}
func (s *Service) Plans(ctx context.Context, project, id string) ([]task.PlanVersion, error) {
	return s.Repository.TaskPlans(ctx, project, id)
}
func (s *Service) Plan(ctx context.Context, project, id, plan string) (task.Plan, error) {
	return s.Repository.TaskPlan(ctx, project, id, plan)
}
