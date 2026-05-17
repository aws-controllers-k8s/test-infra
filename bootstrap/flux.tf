################################################################################
# Flux Bootstrap
#
# A one-shot script that installs a temporary Flux instance, uses it to
# deploy the vendored (self-managed) Flux into flux-system, then tears
# itself down. After bootstrap, Flux manages its own lifecycle.
################################################################################

resource "null_resource" "bootstrap_flux" {
  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
  }

  provisioner "local-exec" {
    command = "${path.module}/scripts/bootstrap-flux.sh ${aws_eks_cluster.this.name} ${var.region} ${path.module}/../charts/flux2-${var.flux_version} ${local.git_repository_url} ${var.test_infra_branch}"

    environment = {
      ACCOUNT_ID          = local.account_id
      STACK_NAME          = local.stack_name
      REGION              = var.region
      CLUSTER_SG_ID       = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
      VPC_ID              = module.vpc.vpc_id
      WEBHOOK_SG_ID       = aws_security_group.prow_webhook_nlb.id
      GHCR_PTC_SECRET_ARN = data.aws_secretsmanager_secret.ghcr_ptc.arn
      PROW_DOMAIN         = var.prow_domain
      PROW_IMAGES_REPO_URI = aws_ecrpublic_repository.prow_images.repository_uri
      TEST_INFRA_ORG      = var.test_infra_org
      TEST_INFRA_REPO     = var.test_infra_repo
      TEST_INFRA_BRANCH   = var.test_infra_branch
      FLUX_VERSION        = var.flux_version
      FLUX_IMAGE_REGISTRY = "${local.account_id}.dkr.ecr.${var.region}.amazonaws.com/fluxcd/fluxcd"
    }
  }

  depends_on = [
    aws_eks_cluster.this, awscc_eks_capability.ack, null_resource.ack_system_namespace
  ]
}

################################################################################
# Flux Kustomization Readiness Validation
#
# Polls until all Kustomizations in flux-system are Ready.
# Runs after Flux bootstrap to ensure the full GitOps tree has reconciled.
################################################################################

resource "null_resource" "validate_kustomizations" {
  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
  }

  provisioner "local-exec" {
    command = "${path.module}/scripts/validate-kustomizations.sh ${aws_eks_cluster.this.name} ${var.region}"
  }

  depends_on = [
    null_resource.bootstrap_flux,
  ]
}
