variable "cluster_name" {
  description = "Name of the EKS cluster"
  type        = string
  default     = "ack-test-infra"
}

variable "cluster_version" {
  description = "Kubernetes version for the EKS cluster"
  type        = string
  default     = "1.35"
}

variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-west-2"
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC. Ignored if vpc_id is set."
  type        = string
  default     = "10.0.0.0/16"
}

variable "vpc_id" {
  description = "Existing VPC ID. If set, skips VPC creation."
  type        = string
  default     = ""
}

variable "subnet_ids" {
  description = "Existing private subnet IDs. Required if vpc_id is set."
  type        = list(string)
  default     = []
}

variable "availability_zones" {
  description = "AZs for the VPC subnets. Ignored if vpc_id is set."
  type        = list(string)
  default     = ["us-west-2a", "us-west-2b"]
}

# Flux versions - pinned here, also stored in flux/flux-version.yaml
variable "flux_version" {
  description = "Flux chart version (must match vendored chart in charts/flux2-<version>/)"
  type        = string
  default     = "2.18.3"
}

variable "git_repository_url" {
  description = "Git repository URL for Flux"
  type        = string
  default     = "https://github.com/gustavodiaz7722/ack-test-infra"
}

variable "git_repository_branch" {
  description = "Git branch for Flux to watch"
  type        = string
  default     = "flux-v2-api-upgrade"
}

variable "flux_path" {
  description = "Path within the git repo for Flux to reconcile"
  type        = string
  default     = "./flux"
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
  description = "Git branch for test-infra repo (used in Prow job extra_refs)"
  type        = string
  default     = "main"
}
