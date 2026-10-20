- name: addon-version-updater
  decorate: true
  # Daily at a fixed time rather than an interval: a version bump rolls the CSI driver
  # DaemonSet that job pods mount secrets through.
  cron: "0 9 * * *"
  annotations:
    description: Sets spec.addonVersion on each managed Addon CR to the default version EKS offers for that cluster's Kubernetes version. This job owns the field; git does not declare it.
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  labels:
    preset-stack-name: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "aws-kubectl") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["bash", "-c"]
        args:
          - |
            set -Eeuo pipefail

            # AWS_REGION comes from the EKS Pod Identity webhook, via periodic-service-account's
            # association; STACK_NAME from the preset-stack-name preset. No defaults, which
            # would silently target the wrong region or stack.
            : "${AWS_REGION:?not set -- expected from the EKS Pod Identity webhook}"
            : "${STACK_NAME:?not set -- expected from the preset-stack-name preset}"

            MANAGER=addon-version-updater
            NAMESPACE=ack-system

            # <cluster> <addon> <Addon CR>. The CRs are declared in
            # flux/ack/charts/{ack-addons,ack-build-infra}; the Role granting this job access
            # to them names the same CRs.
            TARGETS=(
              "${STACK_NAME}-cluster aws-secrets-store-csi-driver-provider secrets-store-csi"
              "${STACK_NAME}-build-cluster aws-secrets-store-csi-driver-provider build-cluster-secrets-store-csi"
            )

            failed=0
            fmt='%-36s %-40s %-34s %-20s %-20s %s\n'
            printf "$fmt" CLUSTER ADDON CR SPEC DEFAULT RESULT

            fail() {
              printf "$fmt" "$cluster" "$addon" "$cr" "${spec:--}" "${default:--}" ERROR
              echo "  ${cr}: $1" >&2
              failed=$((failed + 1))
            }

            for target in "${TARGETS[@]}"; do
              read -r cluster addon cr <<< "$target"
              spec="" default=""

              if ! k8s=$(aws eks describe-cluster --name "$cluster" \
                           --query 'cluster.version' --output text 2>&1); then
                fail "describe-cluster: ${k8s}"; continue
              fi

              # defaultVersion, not the newest: AWS vets the default for each Kubernetes release.
              if ! out=$(aws eks describe-addon-versions --addon-name "$addon" \
                           --kubernetes-version "$k8s" \
                           --query 'addons[0].addonVersions[?compatibilities[0].defaultVersion==`true`].addonVersion | [0]' \
                           --output text 2>&1); then
                fail "describe-addon-versions: ${out}"; continue
              fi
              if [[ -z "$out" || "$out" == "None" ]]; then
                fail "no default version for ${addon} on Kubernetes ${k8s}"; continue
              fi
              default=$out

              if ! obj=$(kubectl get addons.eks.services.k8s.aws "$cr" -n "$NAMESPACE" \
                           --show-managed-fields -o json 2>&1); then
                fail "$obj"; continue
              fi
              spec=$(jq -r '.spec.addonVersion // ""' <<< "$obj")

              crcluster=$(jq -r '.spec.clusterName // ""' <<< "$obj")
              if [[ -n "$crcluster" && "$crcluster" != "$cluster" ]]; then
                fail "targets cluster ${crcluster}, expected ${cluster}"; continue
              fi

              # Another applier owning the field means git still declares it. Argo CD would
              # reassert git's value on its next sync, so overriding it only flaps the addon
              # between versions. Co-owning an identical value is harmless, and is how this
              # job takes the field over before git drops it.
              appliers=$(jq -r --arg m "$MANAGER" '[.metadata.managedFields[]?
                  | select(.operation == "Apply" and .manager != $m)
                  | select(.fieldsV1["f:spec"]["f:addonVersion"] != null) | .manager] | join(",")' <<< "$obj")
              if [[ -n "$appliers" && "$spec" != "$default" ]]; then
                fail "spec.addonVersion is also applied by ${appliers}; remove it from git"; continue
              fi

              # --force-conflicts takes the field back from imperative edits (kubectl edit or
              # patch). Re-applying an unchanged value writes nothing.
              if ! out=$(kubectl apply --server-side --force-conflicts --field-manager="$MANAGER" -f - 2>&1 <<EOF
            apiVersion: eks.services.k8s.aws/v1alpha1
            kind: Addon
            metadata:
              name: ${cr}
              namespace: ${NAMESPACE}
            spec:
              addonVersion: ${default}
            EOF
              ); then
                fail "apply: ${out}"; continue
              fi

              if [[ "$spec" == "$default" ]]; then result="current"; else result="updated from ${spec:-<unset>}"; fi
              printf "$fmt" "$cluster" "$addon" "$cr" "${spec:--}" "$default" "$result"
            done

            if [[ $failed -gt 0 ]]; then
              echo "FAILED: ${failed} of ${#TARGETS[@]} addons." >&2
              exit 1
            fi
