terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.49"
    }
    awscc = {
      source  = "hashicorp/awscc"
      version = ">= 1.0"
    }
    # Was resolved implicitly before argocd-rbac.tf, which is fine until an init picks a
    # different major version. Declared with a floor rather than pinned, matching the
    # others; the lock file holds the exact version.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 3.2"
    }
    time = {
      source  = "hashicorp/time"
      version = ">= 0.11"
    }
  }
}
