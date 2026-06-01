variable "region" {
  description = "AWS region"
  type        = string
}

variable "account_id" {
  description = "AWS account ID for the target environment"
  type        = string
}

variable "flux_version" {
  description = "Flux chart version (must match vendored chart in charts/flux2-<version>/)"
  type        = string
}

variable "prow_domain" {
  description = "Domain for Prow Deck UI (e.g., prow.ack.aws.dev for prod, prow-staging.ack.aws.dev for dev)"
  type        = string
}

variable "test_infra_org" {
  description = "GitHub org for test-infra repo (used in Prow job extra_refs)"
  type        = string
}

variable "test_infra_repo" {
  description = "GitHub repo name for test-infra (used in Prow job extra_refs)"
  type        = string
}

variable "test_infra_branch" {
  description = "Git branch for test-infra repo (used in Prow job extra_refs and Flux)"
  type        = string
}

variable "stage" {
  description = "Deployment stage (e.g., prod, staging, dev)"
  type        = string
}

variable "kubernetes_org" {
  description = "GitHub org that owns the community-operators fork for OLM bundle PRs (e.g., k8s-operatorhub for prod, ack-prow-staging for staging)"
  type        = string
}

variable "redhat_org" {
  description = "GitHub org that owns the community-operators-prod fork for OLM bundle PRs (e.g., redhat-openshift-ecosystem for prod, ack-prow-staging for staging)"
  type        = string
}

variable "controllers" {
  description = "List of ACK controller names to provision ECR public repositories for (non-prod only). Each controller gets a {name}-controller and {name}-chart repo."
  type        = list(string)
}

variable "publish_account_id" {
  description = "AWS account ID that owns the ECR Public repositories for publishing controller images and Helm charts"
  type        = string
}
