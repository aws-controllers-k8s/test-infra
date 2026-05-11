provider "aws" {
  region = var.region
}

provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}

provider "awscc" {
  region = var.region
}

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_secretsmanager_secret" "ghcr_ptc" {
  name = "ecr-pullthroughcache/ghcr-fluxcd"
}

locals {
  account_id          = data.aws_caller_identity.current.account_id
  partition           = data.aws_partition.current.partition
  vpc_id              = var.vpc_id != "" ? var.vpc_id : module.vpc[0].vpc_id
  subnet_ids          = length(var.subnet_ids) > 0 ? var.subnet_ids : module.vpc[0].private_subnets
  prow_images_repo    = "ack-prow-images"
}
