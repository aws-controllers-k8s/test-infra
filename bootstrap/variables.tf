variable "region" {
  description = "AWS region"
  type        = string
}

variable "account_id" {
  description = "AWS account ID for the target environment"
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

variable "seed_ack_bootstrap_policy" {
  description = <<-EOT
    Seed the ACK capability role with the minimal BootstrapPermissions inline policy.

    True only on a FRESH bootstrap, where ACK has not yet adopted the role and needs
    eks:* on the capability and iam:* on itself to get started. ACK replaces that policy
    with its own set once it adopts the role, so leaving this true afterwards makes every
    plan want to recreate a policy ACK has deliberately superseded. See capability.tf.
  EOT
  type        = bool
  default     = false
}

variable "bootstrap_prow_images" {
  description = <<-EOT
    Build and push the Prow images from Terraform.

    True only on a FRESH bootstrap, where the ECR repo is empty and nothing can pull yet.
    It drives two local-exec provisioners: one pushes the builder image, the other runs the
    in-cluster Job that builds the remaining ~15. That takes roughly an hour, and because a
    provisioner re-runs on every replacement, leaving this on means any taint or trigger
    change silently costs that hour. Afterwards Prow's own jobs rebuild the images.
  EOT
  type        = bool
  default     = false
}
