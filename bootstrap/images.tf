################################################################################
# Public ECR Repository for Prow images (non-prod only)
# In production, prow images are published to public.ecr.aws/m5q3e4b2/prow.
# In non-prod stages, we provision a dedicated repo.
# Public ECR is global but managed from us-east-1.
################################################################################

resource "aws_ecrpublic_repository" "prow_images" {
  count    = var.stage != "prod" ? 1 : 0
  provider = aws.us_east_1

  repository_name = local.prow_images_repo

  force_destroy = true
}

################################################################################
# Public ECR Repositories for ACK controller images (non-prod only)
# In production, controllers are published to public.ecr.aws/aws-controllers-k8s.
# In non-prod stages, we provision dedicated repos for each controller in
# var.controllers to test post-submit workflows without writing to production.
################################################################################

resource "aws_ecrpublic_repository" "controller" {
  for_each = var.stage != "prod" ? toset(var.controllers) : toset([])
  provider = aws.us_east_1

  repository_name = "${each.value}-controller"

  force_destroy = true
}

resource "aws_ecrpublic_repository" "chart" {
  for_each = var.stage != "prod" ? toset(var.controllers) : toset([])
  provider = aws.us_east_1

  repository_name = "${each.value}-chart"

  force_destroy = true
}

locals {
  # For non-prod: derive the alias from the first controller repo URI
  # For prod: use the production alias
  controller_ecr_alias    = var.stage != "prod" ? regex("public\\.ecr\\.aws/([^/]+)", aws_ecrpublic_repository.controller[var.controllers[0]].repository_uri)[0] : "aws-controllers-k8s"
  controller_ecr_registry = "public.ecr.aws/${local.controller_ecr_alias}"
  # For non-prod: use the provisioned prow images repo
  # For prod: use the production prow images registry
  prow_images_repo_uri = var.stage != "prod" ? aws_ecrpublic_repository.prow_images[0].repository_uri : "public.ecr.aws/m5q3e4b2/prow"
}

################################################################################
# Prow Images Bootstrap
# Builds and pushes the builder image to public ECR on first apply.
# The in-cluster build job handles building all other images.
################################################################################

resource "null_resource" "bootstrap_prow_images" {
  count = var.stage != "prod" ? 1 : 0

  # Only re-run if these change
  triggers = {
    repository_uri = aws_ecrpublic_repository.prow_images[0].repository_uri
    account_id     = local.account_id
  }

  provisioner "local-exec" {
    command     = "${path.module}/../prow/jobs/images/bootstrap-images.sh"
    environment = {
      AWS_ACCOUNT_ID      = local.account_id
      AWS_REGION          = var.region
      PROW_IMAGE_REPO_URI = aws_ecrpublic_repository.prow_images[0].repository_uri
    }
  }

  depends_on = [aws_ecrpublic_repository.prow_images]
}

################################################################################
# ArtifactReader Role (non-prod only)
# In production, this role exists in the shared publishing account (628432846661).
# In non-prod, we create it locally so the ackdiscover tool can assume it
# to read ECR Public repositories in the same account.
################################################################################

resource "aws_iam_role" "artifact_reader" {
  count = var.stage != "prod" ? 1 : 0

  name = "ArtifactReader"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "artifact_reader_ecr_public" {
  count = var.stage != "prod" ? 1 : 0

  role       = aws_iam_role.artifact_reader[0].name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonElasticContainerRegistryPublicReadOnly"
}

################################################################################
# ArtifactWriter Role (non-prod only)
# In production, this role exists in the shared publishing account (628432846661).
# In non-prod, we create it locally so postsubmit jobs can assume it
# to publish controller images and Helm charts to ECR Public.
################################################################################

resource "aws_iam_role" "artifact_writer" {
  count = var.stage != "prod" ? 1 : 0

  name = "ArtifactWriter"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "artifact_writer_admin" {
  count = var.stage != "prod" ? 1 : 0

  role       = aws_iam_role.artifact_writer[0].name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AdministratorAccess"
}

resource "aws_iam_role_policy_attachment" "artifact_writer_ecr_public" {
  count = var.stage != "prod" ? 1 : 0

  role       = aws_iam_role.artifact_writer[0].name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonElasticContainerRegistryPublicFullAccess"
}

################################################################################
# SSM Parameter for ECR Publish Role ARN (non-prod only)
# In production, this parameter is pre-created manually.
# In non-prod, we bootstrap it pointing to the local ArtifactWriter role
# so release-controller.sh can discover it via the same SSM path.
################################################################################

resource "aws_ssm_parameter" "ecr_publish_role" {
  count = var.stage != "prod" ? 1 : 0

  name  = "/ack/prow/cd/public_ecr/publish_role"
  type  = "String"
  value = aws_iam_role.artifact_writer[0].arn
}
