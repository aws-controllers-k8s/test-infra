################################################################################
# In-cluster RBAC for the Argo CD capability.
#
# Terraform owns this because Argo CD cannot apply the object that authorises Argo CD,
# so it could never be an Application. It belongs here specifically: the SUBJECT these
# objects bind is the kubernetesGroup Terraform adds to the capability role's access
# entry (aws_eks_access_entry.argocd_capability_group in argocd-access.tf), so both
# halves of one mechanism live in one place.
#
# WHY IN-CLUSTER RBAC AT ALL, when EKS access policies do the rest of the work:
# Kubernetes escalation prevention refuses to let a principal create a Role granting
# permissions it does not itself hold, and that check resolves IN-CLUSTER RBAC ONLY. A
# principal authorised entirely by EKS access policies holds nothing as far as the RBAC
# authorizer is concerned, so it cannot create a Role granting anything at all. Most of
# what follows therefore adds no access Argo CD lacked - it restates access the
# associated policies already grant, in the form the RBAC authorizer can see. The two
# real expansions are called out where they appear.
#
# `kubectl auth can-i --as-group=argocd-cluster-scoped` is the right probe for this and
# the wrong probe for anything else: it reports what the RBAC authorizer sees, and
# reports `no` for everything granted by an access policy, because those are enforced by
# the EKS authorizer and are invisible to a SubjectAccessReview. Do not read a `no` from
# it as a gap without checking which authorizer is meant to answer.
################################################################################

locals {
  # The group on the capability role's access entry. The entry's username is
  # session-templated (".../{{SessionName}}") and cannot serve as an RBAC subject, so a
  # group is the only stable option.
  argocd_rbac_group = one(aws_eks_access_entry.argocd_capability_group.kubernetes_groups)

  argocd_grantor_name = "argocd-rbac-grantor"

  # Namespaces holding a CR type that `admin` does not aggregate. Deliberately not the
  # same set as argocd_hub_namespaces: `argocd` gets one despite being covered by no
  # access policy at all.
  argocd_grantor_namespaces = [
    "ack-system",
    "prow",
    "test-pods",
    "argocd",
  ]
}

# Cluster-scoped grants. No associable EKS access policy covers these objects - see the
# block in argocd-access.tf for which were tried and why each fails - so in-cluster RBAC
# is the mechanism rather than another access_policy_association.
resource "kubernetes_cluster_role_v1" "argocd_cluster_scoped" {
  metadata {
    name = "argocd-cluster-scoped"
  }

  # The four cluster-scoped objects in the ack-cluster chart: StorageClass auto-ebs-sc,
  # IngressClass alb, the Karpenter NodePools, and the build cluster's Auto Mode
  # NodeClass (needed because that cluster runs Auto Mode with no built-in NodePools, so
  # no managed `default` NodeClass exists). If that chart stops carrying them, these
  # four rules go too.
  rule {
    api_groups = ["storage.k8s.io"]
    resources  = ["storageclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  rule {
    api_groups = ["networking.k8s.io"]
    resources  = ["ingressclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  rule {
    api_groups = ["karpenter.sh"]
    resources  = ["nodepools"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  rule {
    api_groups = ["eks.amazonaws.com"]
    resources  = ["nodeclasses"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # For prow/namespaces: the prow and test-pods Namespaces. A real grant rather than a
  # restatement, since Namespaces are cluster-scoped and AmazonEKSAdminPolicy is
  # associated per namespace, so it cannot grant creating one.
  #
  # NO DELETE, and that omission is the point: deleting a Namespace cascades to
  # everything inside it, which for `prow` is the whole control plane and for
  # `test-pods` every running job. Argo CD needs to create these and reconcile their
  # labels, never to remove one.
  #
  # The ServiceAccounts on that path need no rule - they are namespaced, and the admin
  # RoleBindings below cover serviceaccounts in prow and test-pods.
  rule {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["get", "list", "watch", "create", "update", "patch"]
  }

  # For prow-plugins. A DELIBERATE PRIVILEGE EXPANSION, and one of the two grants here
  # that is not merely a restatement.
  #
  # prow/plugins/deployments/agent-plugin/rbac.yaml carries a ClusterRole and
  # ClusterRoleBinding. Applying them needs both the ability to create ClusterRoles and
  # holding everything the created ClusterRole grants, or escalation prevention refuses.
  # Cluster-wide prowjobs write and pod read is WIDER than the namespace-scoped admin
  # Argo CD already holds, so the "restatement" argument does not apply here.
  #
  # The exit condition is narrowing the plugin's own ClusterRole to namespaced Roles,
  # which the evidence supports - its Deployment pins PROW_JOB_NAMESPACE to prow and all
  # live ProwJobs are in prow. Not done here because it changes generated RBAC and the
  # plugin's effective permissions, which belongs to whoever owns agent-plugin. If it
  # lands, shrink these rules to match; they only need to remain a superset of what that
  # ClusterRole grants.
  #
  # No `delete` on any of it, and no `escalate`, which would let Argo CD create a Role
  # conferring anything at all.

  # Create the objects. Without this the failure is a plain "cannot create resource
  # clusterroles at the cluster scope", not an escalation error, which is easy to
  # mistake for one.
  rule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources  = ["clusterroles", "clusterrolebindings"]
    verbs      = ["get", "create", "update", "patch"]
  }

  # Hold what the agent-plugin ClusterRole grants, so escalation prevention passes.
  # These mirror it exactly; if that file gains a verb or resource, these need it too or
  # the sync fails.
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

  # For the `secrets` path. THE WIDEST GRANT HERE, authorised explicitly rather than
  # inferred.
  #
  # flux/secrets/secrets-store-rbac.yaml declares a ClusterRole granting the CSI driver
  # all seven verbs on `secrets` cluster-wide. Escalation prevention requires the
  # applier to hold every rule in a ClusterRole it creates, so Argo CD must hold all
  # seven on every Secret in the cluster for that object to apply.
  #
  # BE CLEAR ABOUT WHAT THIS IS: read, write and delete on every Secret in the cluster,
  # including credentials this repo does not own, reachable by anything that can act as
  # the Argo CD capability role. There is no narrower version that still applies the
  # object - a subset fails the escalation check, and the only alternative is the
  # `escalate` verb, which is broader.
  #
  # THE EXIT CONDITION, cheaper than it looks and worth taking: narrow the CSI driver's
  # own ClusterRole to namespaced Roles and this rule can be deleted outright, leaving
  # Argo CD needing only namespaced secrets write, which AmazonEKSAdminPolicy already
  # grants. That is viable today - the ClusterRoleBinding's only subject is the single
  # secrets-store-csi-driver ServiceAccount, and the only SecretProviderClasses are
  # prow/prow-secrets and test-pods/prow-secrets, so the driver writes Secrets in two
  # namespaces rather than all of them.
  #
  # Mirrors that ClusterRole exactly, including delete. If its verbs change, this must
  # change with them.
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

# Namespaced grants: one RoleBinding to the built-in `admin` ClusterRole per namespace.
#
# NOT a privilege increase. AmazonEKSAdminPolicy is already associated to this principal
# scoped to exactly these namespaces (argocd_hub_namespaces, in argocd-access.tf), so a
# RoleBinding to `admin` in them mirrors an access policy the EKS authorizer already
# honours into the authorizer that escalation prevention consults. It replaces an
# open-ended rule list that never converged - each chart adding a Role meant another
# failed sync and another round trip - and any future namespaced Role in those
# namespaces now just works.
#
# for_each over argocd_hub_namespaces rather than a literal list, because equality with
# that list IS the privilege-neutral argument. A namespace added to or removed from the
# access policy changes its binding in the same apply, so the two cannot drift apart and
# quietly falsify the claim above.
#
# Deliberately not done: binding cluster-admin. Only cluster-scoped grants and custom
# resource types still need explicit rules, and both are rare enough that explicitness
# is worth more than brevity.
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

# Custom resource types, which the aggregated `admin` ClusterRole does not cover. One
# Role per namespace, kept as four explicit resources rather than a for_each with
# dynamic rules, because the rules differ per namespace and the point of them is to be
# read.

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
# grant prowjobs including `delete`, which the cluster-scoped rule deliberately omits.
# sinker deletes ProwJobs, so the permission is real, but it is granted here rather than
# cluster-wide.
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

# test-pods: the second SecretProviderClass, mounted by job pods via the CSI driver.
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
# AmazonEKSAdminPolicy is scoped to argocd_hub_namespaces and excludes it, and
# AmazonEKSArgoCDPolicy covers it but is the capability's own-operation grant. So it
# gets no admin binding, and both halves are spelled out: the ability to create roles
# and rolebindings at all, and holding whatever the created Role grants so escalation
# prevention passes.
#
# Granting roles/rolebindings write here does not let Argo CD widen its own access -
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

  # The connection Job amends the AppProject's destinations and applies the
  # prow-build-cluster-resources Application, both runtime work because they name a
  # cluster ACK owns. appprojects is get and patch only, since the AppProject is
  # Terraform-owned and the Job amends one field; applications needs create, because the
  # Job owns that object outright. No delete on either.
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
# symbol in each and the dependency is not inferred - hence the explicit depends_on,
# which keeps a binding from being created before its Role.
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
