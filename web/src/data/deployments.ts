import type { Deployment,DeploymentDetail,DeploymentTarget,DeploymentTargetReview } from "../types";
import { mutation,query,writeResource } from "./resources";

export type DeploymentContainerAction = "start" | "stop" | "restart" | "remove";
export type DeploymentTargetInput = Pick<DeploymentTarget, "workspace_id" | "server_id" | "secret_set_id" | "environment" | "repository" | "git_ref" | "compose_file" | "build_mode"> & {
  source_type: "workspace" | "remote";
  public_url: string;
  health_checks: { type: string; address: string; timeout_seconds: number }[];
};
export const deployments = {
  targets: () => query<DeploymentTarget[]>("/deployment-targets", ["deployments"]),
  list: () => query<Deployment[]>("/deployments", ["deployments"]),
  detail: (id: string) => query<DeploymentDetail>(`/deployments/${id}`, ["deployments"]),
  review: (id: string) => query<DeploymentTargetReview>(`/deployment-targets/${id}/review`, ["deployments"]),
  createTarget: (input: DeploymentTargetInput) => writeResource(mutation("/deployment-targets", "POST", ["deployments"]), input),
  updateTarget: (id: string, input: DeploymentTargetInput) => writeResource(mutation(`/deployment-targets/${id}`, "PUT", ["deployments"]), input),
  deploy: (id: string, commitRef: string) => writeResource(mutation<{deployment: Deployment}>(`/deployment-targets/${id}/deploy`, "POST", ["deployments"]), {commit_ref: commitRef}),
  rollback: (id: string, deploymentID: string) => writeResource(mutation<{deployment: Deployment}>(`/deployment-targets/${id}/rollback`, "POST", ["deployments"]), deploymentID ? {deployment_id: deploymentID} : {}),
  container: (id: string, action: DeploymentContainerAction) => writeResource(mutation(`/deployment-targets/${id}/container`, "POST", ["deployments"]), {action}),
  removeTarget: (id: string) => writeResource(mutation(`/deployment-targets/${id}`, "DELETE", ["deployments"])),
  removeHistory: (id: string) => writeResource(mutation(`/deployments/${id}`, "DELETE", ["deployments"])),
};
