################################################################################
# Flux Namespace
################################################################################

resource "null_resource" "flux_system_namespace" {
  triggers = {
    cluster_name = aws_eks_cluster.this.name
    region       = var.region
  }

  provisioner "local-exec" {
    command = "${path.module}/scripts/bootstrap-namespaces.sh ${aws_eks_cluster.this.name} ${var.region} flux-system"
  }

  depends_on = [aws_eks_cluster.this, module.vpc, awscc_eks_capability.ack, aws_iam_role_policy.ack_capability_bootstrap]
}

################################################################################
# Flux ConfigMaps
#
# Variables consumed by Flux postBuild.substituteFrom across all Kustomizations.
################################################################################

resource "kubernetes_config_map_v1" "self_managed_vars" {
  metadata {
    name      = "self-managed-vars"
    namespace = "flux-system"
  }

  data = {
    STACK_NAME               = local.stack_name
    ACCOUNT_ID               = local.account_id
    REGION                   = var.region
    CLUSTER_SG_ID            = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
    VPC_ID                   = module.vpc.vpc_id
    GHCR_PTC_SECRET_ARN      = data.aws_secretsmanager_secret.ghcr_ptc.arn
    PROW_DOMAIN              = var.prow_domain
    PROW_IMAGES_REPO_URI     = local.prow_images_repo_uri
    TEST_INFRA_ORG           = var.test_infra_org
    TEST_INFRA_REPO          = var.test_infra_repo
    TEST_INFRA_BRANCH        = var.test_infra_branch
    FLUX_IMAGE_REGISTRY      = "${local.account_id}.dkr.ecr.${var.region}.amazonaws.com/fluxcd/fluxcd"
    CONTROLLER_ECR_REGISTRY      = "public.ecr.aws/${local.controller_ecr_alias}"
    PUBLISH_ACCOUNT_ID           = var.publish_account_id
    STAGE                        = var.stage
    KUBERNETES_ORG               = var.kubernetes_org
    REDHAT_ORG                   = var.redhat_org
  }

  depends_on = [null_resource.flux_system_namespace]
}

resource "kubernetes_config_map_v1" "flux_version" {
  metadata {
    name      = "flux-version"
    namespace = "flux-system"
  }

  data = {
    FLUX_VERSION = var.flux_version
  }

  depends_on = [null_resource.flux_system_namespace]

  lifecycle {
    ignore_changes = all
  }
}

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
    command = "${path.module}/scripts/bootstrap-flux.sh ${aws_eks_cluster.this.name} ${var.region} ${path.module}/../charts/flux2-${var.flux_version}"
  }

  depends_on = [
    kubernetes_config_map_v1.self_managed_vars,
    kubernetes_config_map_v1.flux_version,
    null_resource.ack_system_namespace,
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
