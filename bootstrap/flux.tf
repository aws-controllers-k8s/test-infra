################################################################################
# Flux - Install from vendored chart
################################################################################

resource "kubernetes_namespace_v1" "flux_system" {
  metadata {
    name = "flux-system"
  }

  depends_on = [aws_eks_cluster.this, null_resource.flux_suspend, awscc_eks_capability.ack]
}

resource "helm_release" "flux" {
  name       = "flux2"
  namespace  = kubernetes_namespace_v1.flux_system.metadata[0].name
  chart      = "${path.module}/../charts/flux2-${var.flux_version}"

  values = [yamlencode({
    cli = { enabled = false }
  })]

  depends_on = [aws_eks_cluster.this, null_resource.flux_suspend, awscc_eks_capability.ack, kubernetes_config_map_v1.self_managed_vars, kubernetes_config_map_v1.flux_version]
}

# Bootstrap the Flux sync loop. After first sync, Flux manages its own
# source config via flux/flux-self/source.yaml.
resource "kubectl_manifest" "flux_git_source" {
  yaml_body = yamlencode({
    apiVersion = "source.toolkit.fluxcd.io/v1"
    kind       = "GitRepository"
    metadata = {
      name      = "test-infra"
      namespace = "flux-system"
    }
    spec = {
      interval = "1m"
      url      = local.git_repository_url
      ref      = { branch = var.test_infra_branch }
    }
  })

  lifecycle {
    ignore_changes = all
  }

  depends_on = [helm_release.flux]
}

resource "kubectl_manifest" "flux_kustomization" {
  yaml_body = yamlencode({
    apiVersion = "kustomize.toolkit.fluxcd.io/v1"
    kind       = "Kustomization"
    metadata = {
      name      = "test-infra"
      namespace = "flux-system"
    }
    spec = {
      interval = "5m"
      path     = local.flux_path
      prune    = true
      sourceRef = {
        kind = "GitRepository"
        name = "test-infra"
      }
    }
  })

  lifecycle {
    ignore_changes = all
  }

  depends_on = [kubectl_manifest.flux_git_source]
}

################################################################################
# Namespaces + ConfigMaps
################################################################################

resource "kubernetes_namespace_v1" "ack_system" {
  metadata {
    name = "ack-system"
  }

  depends_on = [aws_eks_cluster.this, null_resource.flux_suspend, awscc_eks_capability.ack]
}

resource "kubernetes_config_map_v1" "self_managed_vars" {
  metadata {
    name      = "self-managed-vars"
    namespace = "flux-system"
  }

  data = {
    CLUSTER_NAME            = local.cluster_name
    CLUSTER_SG_ID           = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
    CLUSTER_ADMIN_ROLE_ARN  = aws_iam_role.cluster_admin.arn
    ADMIN_ROLE_ARN          = "arn:${local.partition}:iam::${local.account_id}:role/Admin"
    READONLY_ROLE_ARN       = "arn:${local.partition}:iam::${local.account_id}:role/ReadOnly"
    GHCR_PTC_SECRET_ARN     = data.aws_secretsmanager_secret.ghcr_ptc.arn
    ACK_CAPABILITY_ROLE_ARN = "arn:${local.partition}:iam::${local.account_id}:role/${local.cluster_name}-ack-capability-role"
    ACCOUNT_ID              = local.account_id
    VPC_ID                  = module.vpc.vpc_id
    WEBHOOK_SG_ID           = aws_security_group.prow_webhook_nlb.id
    REGION                  = var.region
    PROW_IMAGES_REPO_NAME   = local.prow_images_repo
    PROW_IMAGES_REPO_URI    = aws_ecrpublic_repository.prow_images.repository_uri
    PROW_IMAGE_REPO         = aws_ecrpublic_repository.prow_images.repository_uri
    PROW_LOGS_BUCKET        = "ack-prow-logs-${local.account_id}"
    TEST_INFRA_ORG          = var.test_infra_org
    TEST_INFRA_REPO         = var.test_infra_repo
    TEST_INFRA_BRANCH       = var.test_infra_branch
  }

  # Flux adds kustomize.toolkit.fluxcd.io labels after sync
  lifecycle {
    ignore_changes = [metadata[0].labels]
  }

  depends_on = [kubernetes_namespace_v1.flux_system]
}

resource "kubernetes_config_map_v1" "flux_version" {
  metadata {
    name      = "flux-version"
    namespace = "flux-system"
  }

  data = {
    FLUX_VERSION = var.flux_version
  }

  # Flux adds kustomize.toolkit.fluxcd.io labels after sync
  lifecycle {
    ignore_changes = [metadata[0].labels]
  }

  depends_on = [kubernetes_namespace_v1.flux_system]
}
