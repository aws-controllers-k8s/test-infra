################################################################################
# Argo CD authorization and cluster registration
#
# Everything here is pure Terraform. No local-exec, no kubectl.
#
# What EKS already did for us when the capability was created (verified live):
#   AmazonEKSArgoCDClusterPolicy  scope=cluster
#   AmazonEKSArgoCDPolicy         scope=namespace, namespaces=[argocd]
#
# Those cover Argo CD's OWN operation - reading its CRs, reading cluster
# registration Secrets, API discovery, namespace creation, CRD create. They do NOT
# grant permission to deploy workloads or to read arbitrary resources for health
# assessment. That is what this file adds.
#
# Authorization is granted via EKS access policies rather than in-cluster RBAC,
# because Argo CD cannot apply the object that authorizes Argo CD. The repo already
# documents this pattern for Flux in flux/ack/build-cluster/access-entries.yaml.
################################################################################

################################################################################
# SCOPE: hub cluster only.
#
# Nothing here touches the build cluster, deliberately. The build cluster is not
# registered as an Argo CD spoke until Phase 4, and when it is, its AccessEntry
# belongs in flux/ack/build-cluster/access-entries.yaml as an ACK CR alongside the
# eight already there - not in Terraform. See docs/argocd-migration.md, D13.
################################################################################

locals {
  argocd_capability_role_arn = aws_iam_role.argocd_capability.arn

  # Namespaces Argo CD may write to on the hub. Exactly the namespaces some Application
  # targets - the list is a statement about what is deployed, not a standing allowance.
  #
  # Two entries have been removed on that basis rather than left to linger:
  #
  #   `prometheus`, when kube-prometheus-stack was removed from the repo.
  #
  #   `flux-system`, once prow-build-cluster-connection moved to ack-system. It was the last
  #   Application targeting that namespace, and it only ever lived there because that is
  #   where it was written - nothing in it is Flux wiring. What remains in flux-system is
  #   Flux's own machinery, which Flux applies and Phase 5 deletes by hand, so Argo CD needs
  #   no access to it.
  #
  # This list also drives the `admin` RoleBindings in argocd-rbac.tf, deliberately: that
  # binding is only defensible as privilege-neutral because it mirrors this association, so
  # the two must move together.
  argocd_hub_namespaces = [
    "ack-system",
    "prow",
    "test-pods",
  ]
}

################################################################################
# Hub cluster (control plane) authorization
################################################################################

# Cluster-wide read for resource discovery and health assessment. Argo CD needs to
# read all resource types cluster-wide even when it writes to only a few namespaces.
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
# aws_eks_access_policy_association is keyed by (cluster, principal, policy), so its
# Terraform ID contains no namespace component:
#   <cluster>#<principal-arn>#<policy-arn>
# A for_each over namespaces therefore produces N resources all contending for the
# same AWS resource. They thrash on every apply and only the last writer survives,
# silently leaving the other namespaces ungranted. namespaces is a list property of
# a single association, not a key.
resource "aws_eks_access_policy_association" "argocd_hub_write" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSAdminPolicy"

  access_scope {
    type       = "namespace"
    namespaces = local.argocd_hub_namespaces
  }
}

################################################################################
# Cluster registration
#
# Registration is a labelled Secret in the capability's namespace. The server field
# must be the EKS cluster ARN - API server URLs and kubernetes.default.svc are not
# supported. No connection credentials are needed; the capability derives them from
# the capability role and the access entries above.
#
# The hub registration must be Terraform-owned: Argo CD cannot deploy anything until
# at least one cluster is registered, so this is the chicken-and-egg seam.
################################################################################

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

################################################################################
# AppProject
#
# spec.sourceNamespaces is REQUIRED by the managed capability and must contain the
# capability's configured namespace. Omitting it does not error clearly - Applications
# in that namespace simply cannot reference the project, surfacing as deployment
# failures. The built-in "default" project is not relied upon for this reason.
#
# kubernetes_manifest validates against the live API at PLAN time, so the Argo CD
# CRDs must already exist. That holds here because the capability created them. On a
# fresh bootstrap the capability and this resource would be in the same apply, so the
# capability must be applied first:
#   terraform apply -target=awscc_eks_capability.argocd
# then a full apply. Only affects an empty account; on an existing stack the CRDs are
# already present.
################################################################################

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

      # Hub only. The build cluster is added as a destination in Phase 4, when it is
      # registered as a spoke - not before. A destination for an unregistered cluster
      # would be misleading.
      # HUB ONLY. Spoke destinations are added at runtime, not here.
      #
      # A destination naming the build cluster is keyed to a cluster ACK owns, and
      # Terraform must not hold anything keyed to it (D13) - the same reason the
      # registration Secret is written by the prow-build-cluster-connection Job rather
      # than by Terraform. Terraform authorises only the cluster it owns and bootstraps.
      #
      # The Job appends the spoke destination, which is why destinations is in
      # ignore_changes below. Without that, the next terraform apply would silently
      # revert the addition and Applications targeting the build cluster would start
      # being rejected.
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

  lifecycle {
    # Spoke destinations are appended at runtime by the prow-build-cluster-connection
    # Job, because a destination naming the build cluster is keyed to a cluster ACK owns
    # and Terraform must not hold that (D13).
    #
    # Without this, every terraform apply would rewrite destinations back to the hub-only
    # list. Nothing would error - the Job re-adds it on its next run - but in between,
    # Applications targeting the build cluster are REJECTED rather than failing at sync,
    # so the symptom appears at a distance from the apply that caused it.
    #
    # Scoped to destinations alone. sourceRepos, sourceNamespaces and
    # clusterResourceWhitelist stay Terraform-managed and drift-corrected.
    ignore_changes = [manifest.spec.destinations]
  }
}

################################################################################
# CRD write exception
#
# AmazonEKSArgoCDClusterPolicy grants customresourcedefinitions `create`, but
# get/update/patch/delete only on Argo CD's OWN CRDs. Any Application that manages a
# third-party CRD therefore installs it successfully on first sync and then fails
# forever on the next one. Confirmed live in staging:
#
#   customresourcedefinitions.apiextensions.k8s.io "..." is forbidden: User
#   ".../ack-test-infra-staging-argocd-capability-role/..." cannot patch resource
#   "customresourcedefinitions" in API group "apiextensions.k8s.io" at the cluster scope
#
# It does not fail fast either - the Application retries indefinitely and sits in
# Running, so the symptom is a stuck sync rather than a clear permission error.
#
# This matters for prow-crds, which installs the ProwJob CRD and must be able to
# refresh it when scripts/upgrade-prow.sh pulls a new version from upstream.
#
# Granted as narrow in-cluster RBAC on exactly one resource type rather than by
# attaching AmazonEKSClusterAdminPolicy, which would hand over the whole cluster.
#
# Terraform must own this: it is the object that authorizes Argo CD, so Argo CD
# cannot apply it (3.4).
################################################################################

# Custom in-cluster RBAC CANNOT be used here, which is worth recording because the
# AWS docs suggest otherwise. The capability's auto-created access entry has:
#
#   kubernetesGroups: []
#   username: arn:aws:sts::<acct>:assumed-role/<role>/{{SessionName}}
#
# There is no group to bind to, and the username is session-templated - at runtime it
# resolves to a fresh value like aws-go-sdk-1787004054908077004. A ClusterRoleBinding
# to "eks-access-entry:<principal-arn>", which the Register-target-clusters docs
# recommend, binds to a group that does not exist and silently grants nothing.
# Verified by attempting exactly that: the CRD patch stayed forbidden.
#
# A separate IAM role does not help either - every capability role gets an identical,
# equally unbindable access entry. The only lever is which access POLICIES are
# associated.
#
# AmazonEKSKROPolicy grants apiextensions.k8s.io/customresourcedefinitions: * . It is
# AWS-managed and far narrower than AmazonEKSClusterAdminPolicy. Its incidental
# grants (kro.run/*, leases, events) are inert here - no kro CRDs are installed.
resource "aws_eks_access_policy_association" "argocd_hub_crd" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSKROPolicy"

  access_scope {
    type = "cluster"
  }
}

################################################################################
# ACK custom resources.
#
# Found by cutting one path over and watching it fail safely:
#
#   pullthroughcacherules.ecr.services.k8s.aws "ghcr-fluxcd" is forbidden: User
#   ".../ack-test-infra-staging-argocd-capability-role/aws-go-sdk-..." cannot patch
#   resource "pullthroughcacherules" in API group "ecr.services.k8s.aws" in the
#   namespace "ack-system"
#
# AmazonEKSAdminPolicy is already associated for the workload namespaces, but the
# AWS-managed policies enumerate standard API groups and do not extend to arbitrary
# CRDs. Argo CD manages 63 ACK custom resources across the *.services.k8s.aws
# groups, so without this it can render them but never apply them.
#
# As established in D14, in-cluster RBAC is not an option: the capability's access
# entry carries no kubernetesGroups and a session-templated username, so there is no
# stable subject to bind. Associated access policies are the only lever.
#
# AmazonEKSACKPolicy is exactly the right shape - it is what the ACK capability role
# itself is granted, and it is scoped to the *.services.k8s.aws groups rather than
# handing out cluster-admin.
#
# Cluster scope, matching the ACK capability role's own association: ACK CRs live in
# ack-system today, but the scope of a policy granting only ACK API groups is already
# narrow, and namespace scope would silently break if a controller is ever pointed at
# another namespace.
################################################################################
resource "aws_eks_access_policy_association" "argocd_hub_ack" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn
  policy_arn    = "arn:${local.partition}:eks::aws:cluster-access-policy/AmazonEKSACKPolicy"

  access_scope {
    type = "cluster"
  }
}

################################################################################
# Cluster-scoped objects in the ack-cluster chart.
#
# Found by cutting one path over and reading the per-resource result. The three
# namespaced ACK CRs synced; all four cluster-scoped objects were refused:
#
#   StorageClass  auto-ebs-sc         SyncFailed  cannot patch StorageClass
#   IngressClass  alb                 SyncFailed  cannot patch IngressClass
#   NodePool      prow-compute        SyncFailed  cannot patch NodePool
#   NodePool      prow-control-plane  SyncFailed  cannot patch NodePool
#
# NO ACCESS POLICY SOLVES THIS. Recorded so the next attempt does not repeat it:
#
#   AmazonEKSBlockStorageClusterPolicy and AmazonEKSComputeClusterPolicy cannot be
#   associated at all - "InvalidParameterException: The specified policyArn can only
#   be associated with service-linked roles". They are reserved for EKS Auto Mode's
#   own service-linked roles. Declaring either as a resource here makes every apply
#   fail, which is why neither appears below.
#
#   AmazonEKSLoadBalancingClusterPolicy associates but grants no IngressClass write.
#   AmazonEKSAdminPolicy grants nothing extra at either scope: it mirrors the built-in
#   admin ClusterRole, which is namespace-oriented and excludes cluster-scoped
#   resources and CRD instances. It also covers no CRD group, so it misses
#   karpenter.sh regardless. Associating it at CLUSTER scope REPLACES the
#   namespace-scoped association above of the same policy, silently widening that
#   grant - verify with list-associated-access-policies after any change.
#
#   The only associable policy covering all four is AmazonEKSClusterAdminPolicy, i.e.
#   cluster-admin for Argo CD.
#
# Solved instead by adding a Kubernetes group to the capability role's access entry
# and binding a narrow ClusterRole to it - see the access entry at the end of this
# file and argocd-rbac.tf.
################################################################################

################################################################################
# A Kubernetes group on the capability role's access entry.
#
# This is the lever that avoids granting Argo CD cluster-admin. An earlier decision
# record concluded in-cluster RBAC was impossible here, because the capability's
# auto-created access entry ships with kubernetesGroups: [] and a session-templated
# username that cannot serve as an RBAC subject. The first half is true; the
# conclusion was not. A group can be ADDED to the entry, and a group is bindable.
#
# With this set, argocd-rbac.tf binds a ClusterRole granting
# exactly storage.k8s.io/storageclasses, networking.k8s.io/ingressclasses,
# karpenter.sh/nodepools and eks.amazonaws.com/nodeclasses - the cluster-scoped
# objects in the ack-cluster chart. Verified: all four sync, and adding the group
# leaves the six associated access policies intact.
#
# Kept as a separate resource rather than folded into an access entry declaration,
# because the entry itself is created by the capability, not by Terraform. Terraform
# only adds the group to it.
################################################################################
resource "aws_eks_access_entry" "argocd_capability_group" {
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = local.argocd_capability_role_arn

  # Must match the subject in argocd-rbac.tf, which consumes this via local.argocd_rbac_group.
  kubernetes_groups = ["argocd-cluster-scoped"]

  lifecycle {
    # The capability owns the entry and sets type and username; Terraform contributes
    # only the group. Without this, Terraform and the capability fight over the rest.
    ignore_changes = [type, user_name]
  }
}
