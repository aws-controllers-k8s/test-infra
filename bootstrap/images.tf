################################################################################
# Public ECR Repository for Prow images (all environments)
# Each environment gets its own ECR Public repo for Prow job images.
# Public ECR is global but managed from us-east-1.
################################################################################

resource "aws_ecrpublic_repository" "prow_images" {
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

################################################################################
# Public ECR Repository for the ACK parent chart (non-prod only)
# In production, the parent chart is published to public.ecr.aws/aws-controllers-k8s/ack-chart.
# In non-prod stages, we provision a dedicated repo.
################################################################################

resource "aws_ecrpublic_repository" "ack_chart" {
  count    = var.stage != "prod" ? 1 : 0
  provider = aws.us_east_1

  repository_name = "ack-chart"

  force_destroy = true
}

locals {
  # For non-prod: derive the alias from the first controller repo URI
  # For prod: use the production alias
  controller_ecr_alias    = var.stage != "prod" ? regex("public\\.ecr\\.aws/([^/]+)", aws_ecrpublic_repository.controller[var.controllers[0]].repository_uri)[0] : "aws-controllers-k8s"
  controller_ecr_registry = "public.ecr.aws/${local.controller_ecr_alias}"
  # All environments use the provisioned prow images repo
  prow_images_repo_uri = aws_ecrpublic_repository.prow_images.repository_uri
  # Builder image tag from images_config.yaml (source of truth)
  prow_build_images_tag = regex("build-prow-images:\\s+(\\S+)", file("${path.module}/../prow/jobs/images_config.yaml"))[0]
}

################################################################################
# Prow Images Bootstrap
# Builds and pushes the builder image to public ECR on first apply.
# The in-cluster build job handles building all other images.
################################################################################

# Same one-shot gate as bootstrap_prow_images_job, which depends on this one: this pushes
# the builder image that the in-cluster Job then uses to build the rest.
resource "null_resource" "bootstrap_prow_images" {
  count = var.bootstrap_prow_images ? 1 : 0

  # Only re-run if these change
  triggers = {
    repository_uri = aws_ecrpublic_repository.prow_images.repository_uri
    account_id     = local.account_id
  }

  provisioner "local-exec" {
    command = "${path.module}/../prow/jobs/images/bootstrap-images.sh"
    environment = {
      AWS_ACCOUNT_ID      = local.account_id
      AWS_REGION          = var.region
      PROW_IMAGE_REPO_URI = aws_ecrpublic_repository.prow_images.repository_uri
    }
  }

  depends_on = [aws_ecrpublic_repository.prow_images]
}

################################################################################
# ArtifactReader Role (non-prod only)
# In production, this role exists in the shared publishing account.
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
      Action    = ["sts:AssumeRole", "sts:TagSession"]
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
# In production, this role exists in the shared publishing account.
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
      Action    = ["sts:AssumeRole", "sts:TagSession"]
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

################################################################################
# Prow Images Publish Role (non-prod only)
# In production, this role exists in the shared publishing account.
# In non-prod, we create it locally so the build-prow-images job can assume it
# to push built Prow images to ECR Public.
################################################################################

resource "aws_iam_role" "publish_prow_images" {
  count = var.stage != "prod" ? 1 : 0

  name = "publish-prow-images"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
}

resource "aws_iam_role_policy_attachment" "publish_prow_images_ecr_public" {
  count = var.stage != "prod" ? 1 : 0

  role       = aws_iam_role.publish_prow_images[0].name
  policy_arn = "arn:${local.partition}:iam::aws:policy/AmazonElasticContainerRegistryPublicFullAccess"
}

resource "aws_ssm_parameter" "prow_ecr_publish_role" {
  count = var.stage != "prod" ? 1 : 0

  name  = "/ack/prow/cd/test-infra/publish-prow-images"
  type  = "String"
  value = aws_iam_role.publish_prow_images[0].arn
}

################################################################################
# Prow Images Build (in-cluster)
#
# One-shot Job that builds and pushes all Prow images using ack-build-tools.
# Runs after kustomization validation ensures pod identities, namespaces, and
# secrets are in place.
################################################################################

# GATED, because "one-shot" was only ever a comment. A local-exec provisioner has no
# concept of having already run: it re-runs whenever the resource is replaced, and a tainted
# instance forces exactly that. This one sat tainted, so every plan wanted to rebuild all 15
# images - roughly an hour - which is why every apply during the migration had to be
# -target'ed to avoid it. `ignore_changes` does not help: it suppresses attribute diffs and
# says nothing about replacement.
#
# Fresh bootstrap:  terraform apply -var bootstrap_prow_images=true
# Afterwards the flag stays off (the default) and images are rebuilt by Prow's own jobs, not
# by Terraform. To force a rebuild deliberately, set the flag again.
resource "null_resource" "bootstrap_prow_images_job" {
  count = var.bootstrap_prow_images ? 1 : 0

  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
  }

  provisioner "local-exec" {
    command = "${path.module}/scripts/bootstrap-prow-images.sh ${aws_eks_cluster.this.name} ${var.region} ${local.prow_images_repo_uri} ${local.prow_build_images_tag}"

    # The script used to read these from the self-managed-vars ConfigMap, which was Flux's
    # substitution source and went with Flux. Passed directly now.
    environment = {
      STACK_NAME        = local.stack_name
      TEST_INFRA_ORG    = var.test_infra_org
      TEST_INFRA_REPO   = var.test_infra_repo
      TEST_INFRA_BRANCH = var.test_infra_branch
    }
  }

  # validate_kustomizations was the other gate here and it is gone with Flux: it polled
  # Flux Kustomizations for Ready, which no longer exist. Nothing replaced it, because what
  # it waited for is now Argo CD's to deliver and Terraform has no handle on that.
  depends_on = [
    null_resource.bootstrap_prow_images,
  ]
}
