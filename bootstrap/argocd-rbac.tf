################################################################################
# In-cluster RBAC for the Argo CD capability.
#
# WHY TERRAFORM OWNS THIS. Argo CD cannot apply the object that authorises Argo CD,
# so this path could never be an Application. It was reconciled by Flux until Phase 5,
# which made it the last thing Flux owned and blocked Flux removal outright. Terraform
# is the right owner rather than the residual one: the SUBJECT these objects bind is
# the kubernetesGroup that Terraform adds to the capability role's access entry
# (aws_eks_access_entry.argocd_capability_group in argocd-access.tf). Both halves of
# one mechanism now live in one place, and neither can be applied without the other
# being visible.
#
# Migrated from flux/argocd/{cluster-scoped-rbac,namespaced-rbac}.yaml, adopted by
# `terraform import` so no object was recreated - a gap in the ClusterRoleBinding is a
# gap in Argo CD's authorisation, and deleting to recreate would have opened one.
#
# WHY IN-CLUSTER RBAC AT ALL, since EKS access policies do the rest of the work here:
# Kubernetes escalation prevention refuses to let a principal create a Role granting
# permissions it does not itself hold, and that check resolves IN-CLUSTER RBAC ONLY.
# A principal authorised entirely by EKS access policies holds nothing as far as the
# RBAC authorizer is concerned, so it cannot create a Role granting anything at all:
#
#   roles.rbac.authorization.k8s.io "build-cluster-connection-write" is forbidden:
#   user ".../ack-test-infra-staging-argocd-capability-role/aws-go-sdk-..."
#   (groups=["argocd-cluster-scoped" "system:authenticated"]) is attempting to grant
#   RBAC permissions not currently held: {APIGroups:[""], Resources:["configmaps"], ...}
#
# Most of what follows therefore adds no access Argo CD lacked. It restates access the
# associated policies already grant, in the form the RBAC authorizer can see. The two
# exceptions are called out where they appear, because they are real expansions.
#
# `kubectl auth can-i --as-group=argocd-cluster-scoped` is the right probe for this and
# the wrong probe for anything else: it reports what the RBAC authorizer sees, which is
# exactly the escalation-prevention view, and it reports `no` for everything granted by
# an access policy, because those are enforced by the EKS authorizer and are invisible
# to a SubjectAccessReview. Do not read a `no` from it as a gap without checking which
# authorizer is meant to be answering.
#
# Full history, including the options rejected: docs/argocd-migration.md.
################################################################################

locals {
  # The group on the capability role's access entry. Every binding here uses it as the
  # subject; aws_eks_access_entry.argocd_capability_group puts it on the entry. The
  # entry's username is session-templated (".../{{SessionName}}") and cannot serve as
  # an RBAC subject, so a group is the only stable option.
  argocd_rbac_group = one(aws_eks_access_entry.argocd_capability_group.kubernetes_groups)

  # One name, used by the four Roles below and by the RoleBindings that reference them.
  argocd_grantor_name = "argocd-rbac-grantor"

  # Namespaces holding a CR type that `admin` does not aggregate. Not the same set as
  # argocd_hub_namespaces: flux-system needs no grantor Role, and `argocd` gets one
  # despite being covered by no access policy at all.
  argocd_grantor_namespaces = [
    "ack-system",
    "prow",
    "test-pods",
    "argocd",
  ]
}

################################################################################
# Cluster-scoped grants.
#
# No associable EKS access policy covers these objects, which is why in-cluster RBAC
# is here at all rather than another access_policy_association:
#
#   AmazonEKSBlockStorageClusterPolicy and AmazonEKSComputeClusterPolicy are rejected
#   outright - "The specified policyArn can only be associated with service-linked
#   roles". AmazonEKSLoadBalancingClusterPolicy associates but grants no IngressClass
#   write. AmazonEKSAdminPolicy mirrors the built-in admin ClusterRole, which is
#   namespace-oriented and excludes cluster-scoped resources and CRD instances; it
#   makes no difference at either scope, and associating it at CLUSTER scope silently
#   REPLACES the namespace-scoped association of the same policy in argocd-access.tf,
#   widening that grant.
#
#   The only associable policy that would cover all of these is
#   AmazonEKSClusterAdminPolicy, i.e. cluster-admin for Argo CD.
################################################################################

resource "kubernetes_cluster_role_v1" "argocd_cluster_scoped" {
  metadata {
    name = "argocd-cluster-scoped"
  }

  ##############################################################################
  # 1-4. The cluster-scoped objects in the ack-cluster chart. If that chart stops
  # carrying them, these four rules go too.
  ##############################################################################

  # StorageClass - auto-ebs-sc
  rule {
    api_groups = ["storage.k8s.io"]
    resources  = ["storageclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # IngressClass - alb
  rule {
    api_groups = ["networking.k8s.io"]
    resources  = ["ingressclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Karpenter NodePool - prow-compute and prow-control-plane on the hub, and the build
  # cluster's pool, since that cluster is registered as a spoke.
  rule {
    api_groups = ["karpenter.sh"]
    resources  = ["nodepools"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # EKS Auto Mode NodeClass - the build cluster's custom prow-compute class. Required
  # because that cluster enables Auto Mode with no built-in NodePools, so no managed
  # `default` NodeClass exists.
  rule {
    api_groups = ["eks.amazonaws.com"]
    resources  = ["nodeclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  ##############################################################################
  # For prow/namespaces: the prow and test-pods Namespaces, plus the ServiceAccounts every
  # PodIdentityAssociation binds to. Those seven objects were applied by Flux directly and
  # by nothing else - the ack-pod-identities CHART excludes them on purpose, since a chart
  # that owns a Namespace can delete one - so they had no owner that survives Flux, and a
  # fresh bootstrap would never have created them.
  #
  # Namespaces are cluster-scoped, so no namespace-scoped access policy reaches them:
  # AmazonEKSAdminPolicy is associated per namespace and cannot grant creating one. This is
  # therefore a real grant rather than a restatement, and it is the smallest one that makes
  # the path work.
  #
  # NO DELETE, and that omission is the point. Deleting a Namespace cascades to everything
  # inside it, which for `prow` is the entire control plane and for `test-pods` is every
  # running job. Argo CD needs to CREATE these and reconcile their labels; it never needs to
  # remove one. `prune` is false on the path as well, so the only way to lose a namespace is
  # by hand.
  #
  # The ServiceAccounts need no rule here: they are namespaced, and the admin RoleBindings
  # below already cover serviceaccounts in prow and test-pods.
  ##############################################################################
  rule {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["get", "list", "watch", "create", "update", "patch"]
  }

  ##############################################################################
  # 5-8. For prow-plugins. THIS IS A DELIBERATE PRIVILEGE EXPANSION, decided rather
  # than drifted into, and one of the two grants here that is NOT merely a
  # restatement of access Argo CD already holds.
  #
  # prow/plugins/deployments/agent-plugin/rbac.yaml carries a ClusterRole and
  # ClusterRoleBinding. Applying them needs two separate permissions: the ability to
  # create ClusterRoles at all, and holding everything the created ClusterRole grants,
  # or escalation prevention refuses it. Measured before deciding, with
  # `kubectl auth can-i --as-group=argocd-cluster-scoped`:
  #
  #   create clusterroles         no    create prowjobs (all ns)   no
  #   create clusterrolebindings  no    patch  prowjobs (all ns)   no
  #                                     list   pods     (all ns)   no
  #                                     get    pods/log (all ns)   no
  #
  # Every other grantor rule in this file is honestly describable as access Argo CD
  # already had, only expressed where escalation prevention can see it -
  # AmazonEKSAdminPolicy grants it namespace-scoped admin over the four namespaces in
  # argocd_hub_namespaces. Cluster-wide prowjobs write and pod read is WIDER than that,
  # so that description does not apply here.
  #
  # The alternative was narrowing the plugin's own ClusterRole to namespaced Roles,
  # which the evidence supports - its Deployment pins PROW_JOB_NAMESPACE to prow and
  # all live ProwJobs are in prow, so the ClusterRole is wider than the workload needs.
  # Not taken here: it changes generated RBAC and the plugin's effective permissions,
  # which belongs to whoever owns agent-plugin. If it lands, shrink these rules to
  # match - they only need to remain a superset of what that ClusterRole grants.
  #
  # What bounds the expansion: kustomize-controller holds strictly more than this and
  # loses it in Phase 5, so the number of broadly-privileged principals does not rise.
  # No `delete` on any of it, and no `escalate` - which would let Argo CD create a Role
  # conferring anything at all.
  ##############################################################################

  # Create the objects themselves. Without this the failure is a plain "cannot create
  # resource clusterroles at the cluster scope", not an escalation error, which is easy
  # to mistake for one.
  rule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources  = ["clusterroles", "clusterrolebindings"]
    verbs      = ["get", "create", "update", "patch"]
  }

  # Hold what the agent-plugin ClusterRole grants, so escalation prevention passes.
  # These mirror it exactly; if that file gains a verb or resource, these need it too
  # or the sync fails.
  rule {
    api_groups = ["prow.k8s.io"]
    resources  = ["prowjobs"]
    verbs      = ["create", "get", "list", "watch", "update", "patch"]
  }

  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["get", "list", "watch"]
  }

  rule {
    api_groups = [""]
    resources  = ["pods/log"]
    verbs      = ["get"]
  }

  ##############################################################################
  # 9. For the `secrets` path. THIS IS THE WIDEST GRANT IN THE MIGRATION, and it was
  # authorised explicitly after being priced, not inferred from a convention.
  #
  # flux/secrets/secrets-store-rbac.yaml declares a ClusterRole granting the CSI
  # driver `secrets` create/delete/get/list/patch/update/watch cluster-wide.
  # Escalation prevention requires the applier to hold EVERY rule in a ClusterRole it
  # creates, so Argo CD must hold all seven verbs on every Secret in the cluster for
  # that object to apply. Measured before asking: all seven were `no`.
  #
  # BE CLEAR ABOUT WHAT THIS IS. It is not read access for health assessment. It is
  # read, write and delete on every Secret in the cluster, including credentials this
  # repo does not own, reachable by anything that can act as the Argo CD capability
  # role. There is no narrower version that still applies the object: a subset fails
  # the escalation check, and the only alternative mechanism is the `escalate` verb,
  # which is broader still.
  #
  # THE EXIT CONDITION, which is cheaper than it looks and should be taken. Narrow the
  # CSI driver's own ClusterRole to namespaced Roles and this rule can be deleted
  # outright - Argo CD would then need only namespaced secrets write, which
  # AmazonEKSAdminPolicy already grants. The measurements say that is viable: the
  # ClusterRoleBinding's only subject is the single secrets-store-csi-driver
  # ServiceAccount in aws-secrets-manager, and the only SecretProviderClasses in the
  # cluster are prow/prow-secrets and test-pods/prow-secrets, so the driver writes
  # Secrets in two namespaces rather than all of them.
  #
  # Mirrors that ClusterRole exactly, including delete. If its verbs change, this must
  # change with them or the sync fails.
  ##############################################################################
  rule {
    api_groups = [""]
    resources  = ["secrets"]
    verbs      = ["create", "delete", "get", "list", "patch", "update", "watch"]
  }
}

resource "kubernetes_cluster_role_binding_v1" "argocd_cluster_scoped" {
  metadata {
    name = "argocd-cluster-scoped"
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role_v1.argocd_cluster_scoped.metadata[0].name
  }

  subject {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Group"
    name      = local.argocd_rbac_group
    namespace = ""
  }
}

################################################################################
# Namespaced grants: one RoleBinding to the built-in `admin` ClusterRole per namespace.
#
# WHY THIS SHAPE, and why it is not a privilege increase. The first four paths were
# unblocked one rule at a time, each discovered by a failed sync. That does not
# converge: every chart that adds a Role adds another round trip. Measured across the
# remaining paths, 13 more triples were missing across two namespaces - and 12 of the
# 13 fall inside the built-in aggregated `admin` ClusterRole.
#
# AmazonEKSAdminPolicy is already associated to this principal scoped to exactly these
# namespaces (argocd_hub_namespaces, in argocd-access.tf). So a RoleBinding to `admin`
# in them grants NO NEW EFFECTIVE PRIVILEGE - it mirrors the access policy the EKS
# authorizer already honours into the authorizer that escalation prevention consults.
# One binding per namespace replaces the open-ended rule list, and any future
# namespaced Role in those namespaces just works.
#
# The coupling works in both directions, which is the point of the for_each: when
# flux-system came off argocd_hub_namespaces - prow-build-cluster-connection having moved to
# ack-system, leaving no Application targeting it - this binding went with it in the same
# apply. Had the list been written out literally here, the binding would have outlived the
# access policy it claims to mirror, and the privilege-neutral argument above would have
# quietly stopped being true.
#
# WHAT IS DELIBERATELY NOT DONE: binding cluster-admin. That would be a genuine
# expansion, and after this change the remaining whack-a-mole is small - only
# cluster-scoped grants and custom resource types need explicit rules, and both are
# rare enough that explicitness is worth more than brevity.
#
# for_each over argocd_hub_namespaces rather than a literal list, because the equality
# with that list is the whole argument for this being privilege-neutral. Adding a
# namespace to the access policy now adds its binding in the same change, and the two
# cannot drift apart silently.
################################################################################

resource "kubernetes_role_binding_v1" "argocd_namespace_admin" {
  for_each = toset(local.argocd_hub_namespaces)

  metadata {
    name      = "argocd-namespace-admin"
    namespace = each.value
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "admin"
  }

  subject {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Group"
    name      = local.argocd_rbac_group
    namespace = ""
  }
}

################################################################################
# Custom resource types, which the aggregated `admin` ClusterRole does not cover. One
# Role per namespace, holding only CR types - kept as four explicit resources rather
# than a for_each with dynamic rules, because the rules differ per namespace and the
# point of them is to be read.
################################################################################

# ack-system: the connection Job's chart reads the build-cluster Cluster CR status.
resource "kubernetes_role_v1" "argocd_grantor_ack_system" {
  metadata {
    name      = local.argocd_grantor_name
    namespace = "ack-system"
  }

  rule {
    api_groups = ["eks.services.k8s.aws"]
    resources  = ["clusters"]
    verbs      = ["get", "list", "watch"]
  }
}

# prow: the secrets path declares a SecretProviderClass here, and prow-config's Roles
# grant prowjobs including `delete`, which the cluster-scoped rule deliberately omits -
# sinker deletes ProwJobs, so the permission is real, but it is granted here rather
# than cluster-wide.
resource "kubernetes_role_v1" "argocd_grantor_prow" {
  metadata {
    name      = local.argocd_grantor_name
    namespace = "prow"
  }

  rule {
    api_groups = ["secrets-store.csi.x-k8s.io"]
    resources  = ["secretproviderclasses"]
    verbs      = ["get", "create", "update", "patch"]
  }

  rule {
    api_groups = ["prow.k8s.io"]
    resources  = ["prowjobs"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
}

# test-pods: the second SecretProviderClass. Job pods mount it via the CSI driver.
resource "kubernetes_role_v1" "argocd_grantor_test_pods" {
  metadata {
    name      = local.argocd_grantor_name
    namespace = "test-pods"
  }

  rule {
    api_groups = ["secrets-store.csi.x-k8s.io"]
    resources  = ["secretproviderclasses"]
    verbs      = ["get", "create", "update", "patch"]
  }
}

# argocd: the one namespace no access policy covers, so nothing here comes for free.
#
# AmazonEKSAdminPolicy is scoped to argocd_hub_namespaces and excludes this one;
# AmazonEKSArgoCDPolicy covers it but is the capability's own-operation grant, which
# reads cluster registration Secrets rather than managing RBAC. So this namespace gets
# no admin binding and needs both halves spelled out:
#
#   1. the ability to create roles and rolebindings at all. Elsewhere the admin binding
#      provides it. Without it the failure is a plain "cannot create resource roles in
#      API group rbac.authorization.k8s.io in the namespace argocd" - not an escalation
#      error, and easy to mistake for one.
#   2. holding whatever the created Role grants, so escalation prevention passes: the
#      secrets, appprojects and applications rules, matching
#      build-cluster-connection-registrar.
#
# Granting roles/rolebindings write here does not let Argo CD widen its own access:
# escalation prevention still caps any Role it creates at what it already holds.
resource "kubernetes_role_v1" "argocd_grantor_argocd" {
  metadata {
    name      = local.argocd_grantor_name
    namespace = "argocd"
  }

  rule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources  = ["roles", "rolebindings"]
    verbs      = ["get", "create", "update", "patch"]
  }

  rule {
    api_groups = [""]
    resources  = ["secrets"]
    verbs      = ["get", "create", "update", "patch"]
  }

  # The connection Job appends the build cluster to the AppProject's destinations, and
  # applies the prow-build-cluster-resources Application. Both are runtime work because
  # they name a cluster ACK owns and Terraform must hold nothing keyed to it (D13).
  # appprojects is get and patch only - the AppProject is Terraform-owned and the Job
  # amends one field - while applications needs create, because the Job owns that object
  # outright. No delete on either.
  rule {
    api_groups = ["argoproj.io"]
    resources  = ["appprojects"]
    verbs      = ["get", "patch"]
  }

  rule {
    api_groups = ["argoproj.io"]
    resources  = ["applications"]
    verbs      = ["get", "create", "update", "patch"]
  }
}

# One binding per grantor Role. All four Roles share a name, so role_ref is the same
# symbol in each; depends_on is implicit through local.argocd_grantor_name only, so the
# Roles are listed explicitly to keep the binding from being created first.
resource "kubernetes_role_binding_v1" "argocd_grantor" {
  for_each = toset(local.argocd_grantor_namespaces)

  metadata {
    name      = local.argocd_grantor_name
    namespace = each.value
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = local.argocd_grantor_name
  }

  subject {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Group"
    name      = local.argocd_rbac_group
    namespace = ""
  }

  depends_on = [
    kubernetes_role_v1.argocd_grantor_ack_system,
    kubernetes_role_v1.argocd_grantor_prow,
    kubernetes_role_v1.argocd_grantor_test_pods,
    kubernetes_role_v1.argocd_grantor_argocd,
  ]
}
