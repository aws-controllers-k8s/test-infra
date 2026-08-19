################################################################################
# The root Application: one object Terraform owns, rendering every other Application
# from git.
#
# WHY THE APPLICATIONS ARE NOT HERE ANY MORE. They were, one kubernetes_manifest per
# path, and the structure they carried - chart path, target namespace, which parameters
# each chart needs, sync behaviour - is all git-authored. That is the `prow-mirror` rule
# applied one level above the charts: static git-authored content belongs with the
# structure, not in Terraform. The consequence that matters day to day is that adding or
# changing a path is now a commit, not a `terraform apply`.
#
# WHAT TERRAFORM STILL HAS TO SUPPLY, and why this is not a full handover. The values
# below are per-environment and Terraform is the only thing that knows them. Argo CD
# cannot read them from the cluster at render time: it renders off-cluster, valuesFrom
# against a ConfigMap does not exist (argo-cd#12060), Helm's lookup returns empty, and
# there is no repo-server here to attach a plugin to. So they travel as ONE helm.values
# blob on this object, and argocd/applications/ threads them into each child.
#
# ONE BLOB, NOT PARAMETERS, and the reason is specific rather than stylistic. Parameters
# are --set, which reads `.` as a path separator and `,` as a list separator. Threading 16
# values through a root chart into 19 children as parameters multiplies every escaping
# hazard this migration already hit. yamlencode also QUOTES every scalar, which is what
# keeps a 12-digit account id a string: unquoted, Go's YAML parser reads 086987147623 as a
# float and printf %s yields %!s(float64=8.6987147623e+10) - a valid-looking image
# reference that fails at pull time, not at render time. Confirmed by writing the same
# file unquoted by hand and watching the chart's guard catch it.
#
# ORDERING IS IN THE CHART, NOT HERE. Each child carries a sync-wave derived from Flux's
# dependsOn graph plus three edges Flux omitted; the root applies them wave by wave. Argo
# CD had no ordering at all before this - no sync-wave on any of the 19 - which the
# migration never noticed because paths were cut over one at a time into a cluster where
# the graph was already satisfied. On a fresh bootstrap it is not. See
# docs/argocd-migration.md for the derivation.
#
# WHAT STAYS TERRAFORM'S, and why each one cannot move:
#
#   the AppProject (argocd-access.tf) - it authorises the repo and the destinations, and
#   every child references it. A child cannot create the project that admits it.
#
#   the hub registration Secret (argocd-access.tf) - Argo CD can deploy nothing until at
#   least one cluster is registered. This is the chicken-and-egg seam.
#
#   in-cluster RBAC (argocd-rbac.tf) - Argo CD cannot apply the objects that authorise
#   Argo CD. Moving it here also fixed an ordering problem by construction: the grants
#   that prow-plugins and secrets need are applied before this object exists at all.
#
#   this object - an Application that renders itself is a loop with no seam. The seam is
#   the point.
#
# WHAT IS DELIBERATELY ABSENT FROM THE CHART:
#
#   prow-build-cluster-resources, permanently. Its destination is the BUILD CLUSTER, and
#   D13 puts anything keyed to that cluster out of Terraform's reach - which now includes
#   anything Terraform renders. It is composed at runtime by the
#   prow-build-cluster-connection Job, which reads the cluster ARN from the CR status,
#   writes the spoke registration Secret and appends the AppProject destination. Adding it
#   to argocd/applications/ would re-introduce exactly what D13 forbids, by a longer route.
#
#   prow-build-cluster-kubeconfig, because it does not outlive Flux. The
#   build-cluster-flux-kubeconfig ConfigMap exists only so kustomize-controller can
#   remote-apply into the build cluster; Phase 5 deletes it and the Access Entry replaces
#   it. Migrating it would mean adopting an object in order to delete it. Contrast
#   flux/prow/build-cluster-connection/, which looks similar and DOES survive, because
#   Prow's own components mount the kubeconfig it writes (D16).
#
#   ack-flux is in that same category and is cut over anyway - done before this was
#   noticed. Its PullThroughCacheRule caches ghcr.io/fluxcd images and Phase 5 removes it.
#   Left alone rather than reverted: it works, and un-migrating it would be churn.
################################################################################

locals {
  # Parameters every chart may draw from, keyed by the chart value name. Sourced from
  # the same locals as self-managed-vars so the two cannot drift while both exist.
  argocd_chart_values = {
    stackName         = local.stack_name
    accountId         = local.account_id
    region            = var.region
    publishAccountId  = var.publish_account_id
    prowDomain        = var.prow_domain
    ghcrPtcSecretArn  = data.aws_secretsmanager_secret.ghcr_ptc.arn
    prowImagesRepoUri = local.prow_images_repo_uri

    # Repo coordinates, needed by prow-build-cluster-connection because the Application it
    # creates at runtime has to name its own source. Terraform uses these for every
    # Application's source already (see repoURL / targetRevision below) and for the
    # AppProject's sourceRepos; passing them as chart values is the same data by the route
    # a runtime owner can reach. They say nothing about the build cluster.
    testInfraOrg    = var.test_infra_org
    testInfraRepo   = var.test_infra_repo
    testInfraBranch = var.test_infra_branch

    # For prow-jobs. The only token on the three generated paths that had no counterpart here
    # and had to be added; same expression as CONTROLLER_ECR_REGISTRY in self-managed-vars, so
    # the two cannot drift while both exist.
    #
    # It is threaded into the substitutor Job's env and passed to envsubst over jobs.yaml, but
    # currently has zero occurrences in that file. Kept because dropping it would change the
    # Job's behaviour on a path being migrated; worth removing from both once confirmed dead.
    controllerEcrRegistry = "public.ecr.aws/${local.controller_ecr_alias}"

    # For prow-config. Plain per-environment scalars the HelmRelease used to substitute.
    stage         = var.stage
    kubernetesOrg = var.kubernetes_org
    redhatOrg     = var.redhat_org

    # Composed here rather than in the chart, because both name resources on the HUB, which
    # Terraform owns and bootstraps. Contrast buildCluster.clusterName, which names the build
    # cluster: the chart does not need it (its only consumer is gated on buildCluster.server,
    # which nothing sets since the connection Job took over that ConfigMap), so nothing has to
    # compose it and D13 never comes up.
    ecrPublicReaderRoleArn = "arn:${local.partition}:iam::${var.publish_account_id}:role/ArtifactReader"
    prowLogsBucketName     = "${local.stack_name}-prow-logs-${local.account_id}"
  }
}

# The one Application Terraform declares.
#
# ServerSideApply is what let this take over the 19 existing Applications without
# recreating them: they were removed from Terraform state rather than destroyed, so the
# live objects were untouched, and SSA merges field ownership instead of replacing the
# object. Verified before the handover with a server-side dry-run apply as
# argocd-controller against all 19 - zero conflicts, because SSA only conflicts where two
# managers set DIFFERENT values and the render was byte-identical to live.
#
# prune stays false, as everywhere. It matters more here than anywhere else: with prune on,
# a chart that renders empty for any reason would delete all 19 Application objects at
# once. Argo CD also cannot delete Applications today - argocd-rbac.tf grants
# get/create/update/patch and no delete - so removing a path from the chart orphans its
# Application rather than removing it. That is deliberate and it is the known cost of this
# pattern; deleting the leftover is a manual step.
#
# selfHeal stays false too, for the reason it is off elsewhere: it reverts live changes Argo
# CD did not make, which during an incident means fighting whoever is diagnosing it.
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
            # Passed through to every child's source, so all 19 read the same revision this
            # object does. A child cannot be pinned elsewhere, which is the point: one
            # branch, one tree.
            project           = kubernetes_manifest.argocd_project.manifest.metadata.name
            repoURL           = "https://github.com/${var.test_infra_org}/${var.test_infra_repo}"
            targetRevision    = var.test_infra_branch
            destinationServer = aws_eks_cluster.this.arn
            argocdNamespace   = "argocd"

            chartValues = local.argocd_chart_values

            # prow-build-cluster-connection's helm.values, composed here because the chart
            # cannot: .Files.Get is chart-rooted, so a chart under argocd/ cannot read
            # prow/jobs/test_config.yaml. Terraform reads it with file() and passes the
            # yamlencode output, which is also what keeps the rendered string identical to
            # what that Application already carries.
            #
            # file() at 226 bytes, precedent at images.tf:63. Verified byte-identical to the
            # live ConfigMap on the build cluster, same single test_config.yaml key.
            testConfigValues = yamlencode({
              testConfig = file("${path.module}/../prow/jobs/test_config.yaml")
            })
          })
        }
      }

      destination = {
        # The capability registers clusters by ARN, not URL. A URL does not match the
        # AppProject destination and the Application is REJECTED rather than failing at
        # sync, so the symptom appears far from the cause.
        server = aws_eks_cluster.this.arn

        # The children are Application objects, so they land in the capability's namespace.
        # It must be in the AppProject's sourceNamespaces or they cannot reference the
        # project.
        namespace = "argocd"
      }

      syncPolicy = {
        syncOptions = [
          "ServerSideApply=true",
          "CreateNamespace=false",
        ]

        # An EMPTY object, not `{prune = false, selfHeal = false}`, and the difference is
        # only about keeping the plan honest. Absent means false to Argo CD, and the API
        # server drops both zero values on Terraform's write - so spelling them out leaves
        # `prune: null -> false` in every future plan, forever, on a field nothing reads.
        # The children can spell them out because argocd-controller's server-side apply
        # keeps them; this object cannot. Presence of `automated` is what enables auto-sync.
        automated = {}
      }
    }
  }

  # The children reference the AppProject by name, and an Application whose project does
  # not exist is rejected.
  depends_on = [kubernetes_manifest.argocd_project]
}
