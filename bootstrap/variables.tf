variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-west-2"
}

variable "account_id" {
  description = "AWS account ID for the target environment"
  type        = string
  default     = "453735116143"
}

# Flux versions - pinned here, also stored in flux/flux-version.yaml
variable "flux_version" {
  description = "Flux chart version (must match vendored chart in charts/flux2-<version>/)"
  type        = string
  default     = "2.18.3"
}

variable "test_infra_org" {
  description = "GitHub org for test-infra repo (used in Prow job extra_refs)"
  type        = string
  default     = "aws-controllers-k8s"
}

variable "test_infra_repo" {
  description = "GitHub repo name for test-infra (used in Prow job extra_refs)"
  type        = string
  default     = "test-infra"
}

variable "test_infra_branch" {
  description = "Git branch for test-infra repo (used in Prow job extra_refs and Flux)"
  type        = string
  default     = "main"
}
