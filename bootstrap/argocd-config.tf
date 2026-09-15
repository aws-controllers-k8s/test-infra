################################################################################
# Argo CD behavioural configuration.
#
# Argo CD removed the built-in health assessment for argoproj.io/Application in 1.8
# (argo-cd#3781) and its docs name the app-of-apps-plus-sync-waves pattern as the
# case that has to restore it. Nothing did, so every child reported no health to
# root-applications - and per the EKS capability docs, a resource with no health
# check is excluded from the parent's health, which lets "sync waves advance before
# those resources are ready". The wave annotations in argocd/applications were
# decorative.
#
# The other half of the fix is pinning children to a resolved SHA, in
# argocd-applications.tf: waves only order a sync of the PARENT, so the parent has
# to be the thing that syncs.
#
# Terraform-owned for the reason argocd-applications.tf lists for the AppProject and
# the rest: it cannot bootstrap itself. A health check delivered by an Application
# would be applied by a wave, and it is what makes waves work. root-applications
# depends on it, so ordering is right from the opening sync.
#
# ACK and kro resources already reported health; the capability ships built-in checks
# for those. argoproj.io/Application was the only blind kind.
################################################################################

resource "kubernetes_config_map_v1" "argocd_cm" {
  metadata {
    name = "argocd-cm"
    # Must match awscc_eks_capability.argocd's configuration.argo_cd.namespace or the
    # capability does not read it.
    namespace = "argocd"
    labels = {
      # REQUIRED, matching upstream Argo CD. Without it the capability ignores the
      # ConfigMap silently: no error, no health, waves still advancing early.
      "app.kubernetes.io/part-of" = "argocd"
    }
  }

  data = {
    # Stricter than the documented snippet, which reads only status.health.status. That
    # is not enough once the parent rewrites child revisions - see the stale guard below.
    # Defaults to Progressing so a child that has not reported holds its wave.
    #
    # No standard Lua libraries: the capability always disables useOpenLibs, so string
    # length uses #, a core operator.
    "resource.customizations.health.argoproj.io_Application" = <<-EOT
      hs = {}
      hs.status = "Progressing"
      hs.message = ""

      if obj.status == nil then
        hs.message = "no status reported yet"
        return hs
      end

      -- Deliberately does NOT gate on operationState.phase == "Running". An Argo CD
      -- operation can wedge indefinitely: on staging ack-cluster sat Running for 27 days
      -- waiting on an AccessEntry that had been removed from its chart, while the app
      -- itself was Synced and Healthy. Treating that as unsettled blocks the wave forever,
      -- and there is no clock in the sandbox to age it out. The revision comparison below
      -- is the real settling check.
      if obj.status.sync == nil then
        hs.message = "no sync status yet"
        return hs
      end

      -- OutOfSync normally means the child has not applied its changes, so hold the wave.
      -- The exception is an orphan waiting on a prune that is never rendered anywhere in
      -- this repo: that app can never reach Synced, so treating it as unsettled blocks
      -- every later wave permanently. Hit on staging, where prow-namespaces held wave 0
      -- forever over a ServiceAccount/workflow-runner that had moved to another chart.
      -- Only tolerated when EVERY out-of-sync resource is a pending prune; anything else
      -- still holds.
      if obj.status.sync.status ~= "Synced" then
        local blocking = true
        if obj.status.resources ~= nil then
          blocking = false
          for _, r in ipairs(obj.status.resources) do
            if r.status ~= nil and r.status ~= "Synced" and r.requiresPruning ~= true then
              blocking = true
              break
            end
          end
        end
        if blocking then
          hs.message = "not synced"
          return hs
        end
      end

      -- Stale guard. Just after the parent rewrites a child's targetRevision the child
      -- still carries Synced and Healthy from its PREVIOUS revision, and the parent
      -- would advance the wave on that. Only compared when a full SHA was pinned: a
      -- branch-pinned child never compares equal and would block its wave forever.
      if obj.spec ~= nil and obj.spec.source ~= nil and obj.spec.source.targetRevision ~= nil then
        if #obj.spec.source.targetRevision == 40 then
          if obj.status.sync.revision ~= obj.spec.source.targetRevision then
            hs.message = "synced at a different revision"
            return hs
          end
        end
      end

      if obj.status.health ~= nil then
        hs.status = obj.status.health.status
        if obj.status.health.message ~= nil then
          hs.message = obj.status.health.message
        end
      end
      return hs
    EOT
  }

  depends_on = [awscc_eks_capability.argocd]
}
