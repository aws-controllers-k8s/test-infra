provider "aws" {
  region = var.region

  # ACK adds eks:* tags to resources it manages. Ignore them so Terraform
  # doesn't fight with ACK on every apply.
  ignore_tags {
    key_prefixes = ["eks:"]
  }
}

provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}

provider "awscc" {
  region = var.region
}

provider "kubernetes" {
  host                   = aws_eks_cluster.this.endpoint
  cluster_ca_certificate = base64decode(aws_eks_cluster.this.certificate_authority[0].data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", aws_eks_cluster.this.name, "--region", var.region]
  }
}

data "aws_partition" "current" {}
data "aws_secretsmanager_secret" "ghcr_ptc" {
  name = "ecr-pullthroughcache/ghcr-fluxcd"
}

locals {
  account_id         = var.account_id
  partition          = data.aws_partition.current.partition
  prow_images_repo   = "${local.stack_name}-prow-images"
  cluster_name       = "${local.stack_name}-cluster"
  stack_name         = "ack-test-infra-${var.stage}"
  cluster_version    = "1.35"
  flux_path          = "./flux"
  git_repository_url = "https://github.com/${var.test_infra_org}/${var.test_infra_repo}"
}
