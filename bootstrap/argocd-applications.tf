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

    # Sandbox account whose agent-e2e-test-role the build-cluster add-resource agent
    # assumes for e2e. Already a string (see variable), so it does not trip the
    # 12-digit-account-id-as-float hazard the chartValues comment warns about. Empty in
    # environments not running agent e2e, which omits the grant (prow-iam-roles.yaml).
    agentE2eAccountId = var.agent_e2e_account_id

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
          # Children are pinned to the resolved SHA; this object keeps tracking the branch,
          # because something has to notice new commits. While the children tracked it too, a
          # commit changing only chart CONTENTS rendered identical child specs, so this
          # Application never had drift, never synced, and each child synced itself unordered.
          # Waves are only evaluated inside a sync of this object, so none was ever processed.
          # Pinned, every commit changes all 19 child specs, so the rollout is always ordered
          # by wave and a child that will not go Healthy stops everything behind it.
          #
          # A PARAMETER, not part of the values blob: build-env interpolation only happens in
          # helm.parameters. forceString so an all-digit SHA does not arrive as a number.
          parameters = [
            {
              name        = "childRevision"
              value       = "$ARGOCD_APP_REVISION"
              forceString = true
            },
          ]
          values = yamlencode({
            project = kubernetes_manifest.argocd_project.manifest.metadata.name
            repoURL = "https://github.com/${var.test_infra_org}/${var.test_infra_repo}"
            # Retained although the chart now reads childRevision instead. Passing both
            # means this object and the chart can be rolled forward or back in either
            # order: dropping it would leave a Terraform-first apply feeding the old
            # chart, which requires it, nothing - and a git-first merge feeding the new
            # chart, which requires childRevision, nothing. Either way the root's render
            # fails and every deploy stops.
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

  # Terraform owns this object's spec, so it has to win against out-of-band edits. Without
  # this, an apply that changes a field someone patched by hand fails outright:
  #
  #   Error: field manager conflict ... conflict with "kubectl-patch" using
  #   argoproj.io/v1alpha1: .spec.source.targetRevision
  #
  # Hit on staging, where targetRevision had been kubectl-patched to a feature branch, so
  # the field was owned by kubectl-patch and Terraform could not move it back. The
  # alternative is that a single manual patch makes this resource permanently
  # unmanageable, which is worse: this is the object the whole app-of-apps renders from.
  # Only fields Terraform actually sets are forced; eks-capability keeps the ones it owns.
  field_manager {
    force_conflicts = true
  }

  # A child Application whose project does not exist is rejected. argocd_cm has to be in
  # place before the opening sync, or the waves do nothing for it - see argocd-config.tf.
  depends_on = [
    kubernetes_manifest.argocd_project,
    kubernetes_config_map_v1.argocd_cm,
  ]
}
