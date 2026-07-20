package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestBlankProjectProvisionCommitCreatesManagedWorkspace(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	server := createOperationTestServer(t, database, "blank-server", "blank-token")
	provision, err := database.CreateBlankProject(ctx, server.ID, "blank", "apps/blank", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if provision.Project.Status != "provisioning" || provision.WorkspaceID == "" || provision.OperationID == "" || provision.ServerID != server.ID {
		t.Fatalf("unexpected provisioning result: %#v", provision)
	}
	operation, err := database.Operation(ctx, provision.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Kind != gitProjectCreateKind || operation.ProjectID != provision.Project.ID || operation.WorkspaceID != "" {
		t.Fatalf("unexpected creation operation: %#v", operation)
	}
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil || command.ProjectID != provision.Project.ID || command.WorkspaceID != provision.WorkspaceID || command.Destination != "apps/blank" {
		t.Fatalf("unexpected creation command: %#v %v", command, err)
	}
	result := protocol.GitProjectCreateResult{Path: "/var/lib/wio-agent/projects/apps/blank", Branch: "main", Unborn: true}
	resultPayload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CommitBlankProject(ctx, operation, command, result, protocol.OperationResult{OperationID: operation.ID, Status: "succeeded", Data: resultPayload}); err != nil {
		t.Fatal(err)
	}
	project, err := database.Project(ctx, provision.Project.ID)
	if err != nil || project.Status != "ready" || project.ProvisionError != "" {
		t.Fatalf("unexpected committed project: %#v %v", project, err)
	}
	workspace, err := database.Workspace(ctx, provision.WorkspaceID)
	if err != nil || workspace.ManagementMode != "managed" || workspace.Kind != "primary" || workspace.Path != result.Path || workspace.Branch != "main" || workspace.CommitSHA != "" {
		t.Fatalf("unexpected committed workspace: %#v %v", workspace, err)
	}
	completed, err := database.Operation(ctx, operation.ID)
	if err != nil || completed.Status != "succeeded" || completed.CompletedAt == nil || completed.ResultData != string(resultPayload) {
		t.Fatalf("unexpected completed operation: %#v %v", completed, err)
	}
}

func TestBlankProjectFailureCanBeRetriedWithSameWorkspaceIdentity(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	server := createOperationTestServer(t, database, "retry-blank-server", "retry-blank-token")
	provision, err := database.CreateBlankProject(ctx, server.ID, "retry-blank", "retry-blank", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, provision.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	command := provision.Command
	failure := protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: "destination already exists"}
	if err := database.FailBlankProject(ctx, operation, command, failure, "failed"); err != nil {
		t.Fatal(err)
	}
	failed, err := database.Project(ctx, provision.Project.ID)
	if err != nil || failed.Status != "failed" || failed.ProvisionError != failure.Message {
		t.Fatalf("unexpected failed project: %#v %v", failed, err)
	}
	retry, err := database.RetryBlankProject(ctx, provision.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.OperationID == provision.OperationID || retry.WorkspaceID != provision.WorkspaceID || retry.Command.ProjectID != provision.Project.ID || retry.Command.Destination != command.Destination || retry.ServerID != server.ID || retry.Project.Status != "provisioning" {
		t.Fatalf("retry did not preserve creation identity: %#v", retry)
	}
	if _, err := database.RetryBlankProject(ctx, provision.Project.ID); err == nil {
		t.Fatal("active retry should be rejected")
	}
}

func TestValidateBlankProjectResultRejectsMismatches(t *testing.T) {
	command := protocol.GitProjectCreateCommand{ProjectID: "project", WorkspaceID: "workspace", InitialBranch: "main", InitializeREADME: false}
	valid := protocol.GitProjectCreateResult{Path: "/srv/project", Branch: "main", Unborn: true}
	if err := ValidateBlankProjectResult(command, valid); err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]protocol.GitProjectCreateResult{
		"relative path":     {Path: "srv/project", Branch: "main", Unborn: true},
		"wrong branch":      {Path: "/srv/project", Branch: "develop", Unborn: true},
		"unexpected commit": {Path: "/srv/project", Branch: "main", CommitSHA: "abc", Unborn: false},
		"unexpected remote": {Path: "/srv/project", Branch: "main", Unborn: true, RemoteURL: "https://example.com/repo.git"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBlankProjectResult(command, result); err == nil {
				t.Fatal("expected result mismatch")
			}
		})
	}
	readmeCommand := command
	readmeCommand.InitializeREADME = true
	if err := ValidateBlankProjectResult(readmeCommand, protocol.GitProjectCreateResult{Path: "/srv/project", Branch: "main", Unborn: true}); err == nil {
		t.Fatal("README creation must return an initial commit")
	}
}
