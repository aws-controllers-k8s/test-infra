################################################################################
# Argo CD authorization and cluster registration. Pure Terraform; no local-exec.
#
# Creating the capability already grants AmazonEKSArgoCDClusterPolicy (cluster) and
# AmazonEKSArgoCDPolicy (namespace=argocd). Those cover Argo CD's own operation -
# reading its CRs and registration Secrets, API discovery, namespace and CRD create.
# They do NOT grant deploying workloads or reading arbitrary resources for health
# assessment, which is what this file adds.
#
# Granted through EKS access policies and one added Kubernetes group rather than
# in-cluster RBAC alone, because Argo CD cannot apply the objects that authorize
# Argo CD.
#
# Hub cluster only. Anything keyed to the build cluster is owned at runtime, never
# by Terraform, because its ARN is not knowable until ACK creates the cluster; its
# access entry belongs with the other ACK CRs in
# flux/ack/build-cluster/access-entries.yaml.
################################################################################

locals {
  argocd_capability_role_arn = aws_iam_role.argocd_capability.arn

  # Namespaces Argo CD may write to. Exactly the namespaces some Application targets,
  # so this is a statement about what is deployed rather than a standing allowance.
  #
  # This list also drives the `admin` RoleBindings in argocd-rbac.tf, and that binding
  # is only defensible as privilege-neutral because it mirrors this association. The
  # two must move together.
  argocd_hub_namespaces = [
    "ack-system",
    "prow",
    "test-pods",
  ]
}

# Cluster-wide read, for resource discovery and health assessment. Needed even though
# Argo CD writes to only a few namespaces.
resource "aws_eks_access_policy_association" "argocd_hub_read" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSAdminViewPolicy"

  access_scope {
    type = "cluster"
  }
}

# Write access, namespace-scoped.
#
# ONE association carrying the full namespace list - NOT for_each over namespaces.
# This resource is keyed by (cluster, principal, policy), so its Terraform ID has no
# namespace component. A for_each therefore produces N resources contending for the
# same AWS resource: they thrash on every apply and only the last writer survives,
# silently leaving the other namespaces ungranted.
resource "aws_eks_access_policy_association" "argocd_hub_write" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSAdminPolicy"

  access_scope {
    type       = "namespace"
    namespaces = local.argocd_hub_namespaces
  }
}

# Cluster registration: a labelled Secret in the capability's namespace. `server` must
# be the EKS cluster ARN - API server URLs and kubernetes.default.svc are not
# supported. No credentials; the capability derives them from the capability role and
# the access entries above.
#
# Terraform-owned because Argo CD can deploy nothing until a cluster is registered.
resource "kubernetes_secret_v1" "argocd_cluster_hub" {
  metadata {
    name      = "in-cluster"
    namespace = "argocd"

    labels = {
      "argocd.argoproj.io/secret-type" = "cluster"
    }
  }

  data = {
    name    = "in-cluster"
    server  = aws_eks_cluster.this.arn
    project = kubernetes_manifest.argocd_project.manifest.metadata.name
  }

  depends_on = [awscc_eks_capability.argocd]
}

# AppProject.
#
# spec.sourceNamespaces is REQUIRED by the managed capability and must contain the
# capability's namespace. Omitting it does not error clearly - Applications in that
# namespace simply cannot reference the project, which surfaces as deployment failures.
#
# kubernetes_manifest validates against the live API at PLAN time, so the Argo CD CRDs
# must already exist. On a fresh bootstrap, apply the capability first
# (-target=awscc_eks_capability.argocd) and then apply fully.
resource "kubernetes_manifest" "argocd_project" {
  manifest = {
    apiVersion = "argoproj.io/v1alpha1"
    kind       = "AppProject"

    metadata = {
      name      = "test-infra"
      namespace = "argocd"
    }

    spec = {
      description = "ACK test-infra platform applications"

      sourceNamespaces = ["argocd"]

      sourceRepos = [
        "https://github.com/${var.test_infra_org}/${var.test_infra_repo}",
      ]

      # HUB ONLY. The spoke destination is appended at runtime by the
      # prow-build-cluster-connection Job, because it names a cluster ACK owns. See
      # computed_fields below before changing this list.
      destinations = [
        {
          server    = aws_eks_cluster.this.arn
          namespace = "*"
        },
      ]

      clusterResourceWhitelist = [
        {
          group = "*"
          kind  = "*"
        },
      ]
    }
  }

  depends_on = [awscc_eks_capability.argocd]

  # spec.destinations is written by the connection Job, so Terraform must not
  # reconcile it away.
  #
  # `lifecycle { ignore_changes = [manifest.spec.destinations] }` DOES NOT WORK here.
  # ignore_changes substitutes the prior STATE value for the CONFIG value of an
  # argument, and both say hub-only - the divergence is in `object`, which is computed,
  # and ignore_changes cannot reach a computed attribute. The provider then applies
  # `manifest` through server-side apply, and destinations is an atomic list with no
  # merge key, so applying a one-entry list means "the list is exactly this".
  #
  # Whichever manager wrote the field last decides how that breaks: if the Job did,
  # the apply fails on a field-manager conflict and takes argocd_root with it, since
  # that resource depends on this one; if Terraform did, the apply succeeds and absorbs
  # the spoke ARN into state, which is the coupling this list exists to avoid.
  #
  # computed_fields is the provider's mechanism for a field written by someone else.
  # The two metadata entries are its DEFAULT and must be restated - supplying
  # computed_fields replaces the default list rather than appending to it, and dropping
  # them reintroduces churn from annotations Argo CD and ACK write. sourceRepos,
  # sourceNamespaces and clusterResourceWhitelist stay Terraform-managed.
  computed_fields = ["metadata.annotations", "metadata.labels", "spec.destinations"]
}

# CRD write.
#
# AmazonEKSArgoCDClusterPolicy grants customresourcedefinitions `create`, but
# get/update/patch/delete only on Argo CD's OWN CRDs. An Application managing a
# third-party CRD therefore installs it on first sync and fails forever after, and it
# does not fail fast - the Application retries and sits in Running, so the symptom is
# a stuck sync rather than a permission error. This matters for prow-crds, which must
# refresh the ProwJob CRD when scripts/upgrade-prow.sh pulls a new version.
#
# AmazonEKSKROPolicy grants apiextensions.k8s.io/customresourcedefinitions: * and is
# far narrower than AmazonEKSClusterAdminPolicy. Its incidental grants (kro.run/*,
# leases, events) are inert - no kro CRDs are installed.
resource "aws_eks_access_policy_association" "argocd_hub_crd" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSKROPolicy"

  access_scope {
    type = "cluster"
  }
}

# ACK custom resources. The AWS-managed policies enumerate standard API groups and do
# not extend to arbitrary CRDs, so without this Argo CD renders the ~63 ACK CRs it
# manages but cannot apply them. AmazonEKSACKPolicy is what the ACK capability role
# itself is granted and is scoped to the *.services.k8s.aws groups.
#
# Cluster scope, matching the ACK capability role: the policy is already narrow, and
# namespace scope would break silently if a controller is pointed elsewhere.
resource "aws_eks_access_policy_association" "argocd_hub_ack" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSACKPolicy"

  access_scope {
    type = "cluster"
  }
}

# Cluster-scoped objects in the ack-cluster chart - StorageClass, IngressClass and the
# two NodePools - are handled by the group below plus a narrow ClusterRole in
# argocd-rbac.tf, NOT by an access policy. Recorded so the next attempt does not
# repeat it:
#
#   AmazonEKSBlockStorageClusterPolicy and AmazonEKSComputeClusterPolicy cannot be
#   associated at all ("policyArn can only be associated with service-linked roles").
#   Declaring either makes every apply fail.
#
#   AmazonEKSLoadBalancingClusterPolicy grants no IngressClass write.
#
#   AmazonEKSAdminPolicy adds nothing at either scope - it mirrors the namespace-
#   oriented admin ClusterRole and covers no CRD group. Associating it at CLUSTER
#   scope also REPLACES the namespace-scoped association above, silently widening that
#   grant; verify with list-associated-access-policies after any change.
#
#   The only associable policy covering all four is AmazonEKSClusterAdminPolicy.

# A Kubernetes group on the capability role's access entry, which is what avoids
# granting Argo CD cluster-admin.
#
# The capability's auto-created entry ships with kubernetesGroups: [] and a
# session-templated username that resolves to a fresh value per session, so it cannot
# itself serve as an RBAC subject - binding to "eks-access-entry:<principal-arn>", as
# the register-target-clusters docs suggest, binds a group that does not exist and
# grants nothing. A group can however be ADDED to the entry, and a group is bindable.
#
# Separate resource because the capability creates the entry; Terraform only adds the
# group.
resource "aws_eks_access_entry" "argocd_capability_group" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn

  # Must match the subject in argocd-rbac.tf, via local.argocd_rbac_group.
  kubernetes_groups = ["argocd-cluster-scoped"]

  lifecycle {
    # The capability owns the entry and sets type and username; Terraform contributes
    # only the group.
    ignore_changes = [type, user_name]
  }
}
