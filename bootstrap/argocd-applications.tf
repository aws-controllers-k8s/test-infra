################################################################################
# The root Application. The only Application Terraform declares; it renders the
# rest from argocd/applications/ in git, so adding or changing a path is a commit
# rather than a `terraform apply`.
#
# Terraform still supplies the per-environment values, because Argo CD renders
# off-cluster and cannot read them from the cluster: valuesFrom against a
# ConfigMap does not exist (argo-cd#12060), Helm's lookup returns empty, and there
# is no repo-server to attach a plugin to. They travel as ONE helm.values blob,
# not as parameters: yamlencode quotes every scalar, which is what keeps a
# 12-digit account id a string. Unquoted, an id with a leading zero parses as a
# float and renders as %!s(float64=...) - a valid-looking image reference that
# fails at pull time rather than at render time.
#
# Sync ordering lives in the chart, as a sync-wave per child.
#
# Terraform keeps four things because none of them can bootstrap themselves: the
# AppProject and the hub registration Secret (argocd-access.tf), in-cluster RBAC
# (argocd-rbac.tf), and this object.
#
# prow-build-cluster-resources is deliberately absent from the chart. Anything
# keyed to the build cluster is owned at runtime, never by Terraform, because its
# ARN is not knowable until ACK creates the cluster. The
# prow-build-cluster-connection Job composes that Application instead, reading the
# ARN from the CR status.
################################################################################

locals {
  # Values any chart may draw from, keyed by chart value name.
  argocd_chart_values = {
    stackName         = local.stack_name
    accountId         = local.account_id
    region            = var.region
    publishAccountId  = var.publish_account_id
    prowDomain        = var.prow_domain
    prowImagesRepoUri = local.prow_images_repo_uri

    # Needed by prow-build-cluster-connection: the Application it creates at
    # runtime has to name its own source.
    testInfraOrg    = var.test_infra_org
    testInfraRepo   = var.test_infra_repo
    testInfraBranch = var.test_infra_branch

    # For prow-jobs. Passed to envsubst over jobs.yaml, where it currently has zero
    # occurrences - worth removing from both once confirmed dead.
    controllerEcrRegistry = "public.ecr.aws/${local.controller_ecr_alias}"

    # For prow-config.
    stage         = var.stage
    kubernetesOrg = var.kubernetes_org
    redhatOrg     = var.redhat_org

    # Composed here because both name resources on the hub, which Terraform owns.
    ecrPublicReaderRoleArn = "arn:${local.partition}:iam::${var.publish_account_id}:role/ArtifactReader"
    prowLogsBucketName     = "${local.stack_name}-prow-logs-${local.account_id}"
  }
}

# prune stays false. With prune on, a chart that rendered empty for any reason
# would delete every child Application at once. Argo CD also has no delete on
# Applications (see argocd-rbac.tf), so removing a path from the chart orphans its
# Application rather than removing it - deleting the leftover is a manual step.
#
# selfHeal stays false so it does not revert live changes during a diagnosis.
resource "kubernetes_manifest" "argocd_root" {
  manifest = {
    apiVersion = "argoproj.io/v1alpha1"
    kind       = "Application"

    metadata = {
      name      = "root-applications"
      namespace = "argocd"
    }

    spec = {
      project = kubernetes_manifest.argocd_project.manifest.metadata.name

      source = {
        repoURL        = "https://github.com/${var.test_infra_org}/${var.test_infra_repo}"
        targetRevision = var.test_infra_branch
        path           = "argocd/applications"

        helm = {
          values = yamlencode({
            # Passed through to every child's source, so all of them read the same
            # revision this object does. One branch, one tree.
            project           = kubernetes_manifest.argocd_project.manifest.metadata.name
            repoURL           = "https://github.com/${var.test_infra_org}/${var.test_infra_repo}"
            targetRevision    = var.test_infra_branch
            destinationServer = aws_eks_cluster.this.arn
            argocdNamespace   = "argocd"

            chartValues = local.argocd_chart_values

            # Composed here because the chart cannot: .Files.Get is chart-rooted, so
            # a chart under argocd/ cannot read prow/jobs/test_config.yaml.
            testConfigValues = yamlencode({
              testConfig = file("${path.module}/../prow/jobs/test_config.yaml")
            })
          })
        }
      }

      destination = {
        # The capability registers clusters by ARN, not URL. A URL does not match
        # the AppProject destination and the Application is REJECTED rather than
        # failing at sync, so the symptom appears far from the cause.
        server = aws_eks_cluster.this.arn

        # The children are Application objects, so they land in the capability's
        # namespace, which must be in the AppProject's sourceNamespaces.
        namespace = "argocd"
      }

      syncPolicy = {
        syncOptions = [
          "ServerSideApply=true",
          "CreateNamespace=false",
        ]

        # An EMPTY object, not `{prune = false, selfHeal = false}`. Absent means
        # false to Argo CD, and the API server drops both zero values on
        # Terraform's write - so spelling them out would leave `prune: null ->
        # false` in every future plan. Presence of `automated` enables auto-sync.
        automated = {}
      }
    }
  }

  # A child Application whose project does not exist is rejected.
  depends_on = [kubernetes_manifest.argocd_project]
}
