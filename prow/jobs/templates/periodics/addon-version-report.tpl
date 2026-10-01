- name: addon-version-report
  decorate: true
  # Report-only. Daily at 09:00 UTC so the result is seen during the working day.
  cron: "0 9 * * *"
  annotations:
    description: Reports, per cluster, whether a managed EKS addon is behind the default version EKS offers for that cluster's Kubernetes version. Reports only; changes nothing.
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      # Reuses the integration-test image because it already carries the AWS CLI, the
      # same image-reuse precedent as upgrade-eks-distro-version. This job needs no
      # kubectl and no cluster credentials -- everything it reads comes from the EKS
      # API, so it is pure-read and cannot affect either cluster.
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "integration-test") }}
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

            REGION="${AWS_REGION:-us-west-2}"

            # Addons whose version this stack manages, i.e. the ones declared as Addon CRs
            # in flux/ack/charts/{ack-addons,ack-build-infra}. Keep in step with those charts.
            ADDONS=("aws-secrets-store-csi-driver-provider")

            # Clusters are discovered rather than named. The cluster name is not available
            # here: envsubst only substitutes TEST_INFRA_ORG/REPO/BRANCH, the image repo
            # and a few version vars, so a ${PROW_CLUSTER_NAME}-style placeholder would
            # reach the container as a literal. Discovery also keeps the template
            # stage-agnostic and notices a cluster nobody remembered to add.
            # Assign in two steps rather than `mapfile < <(aws ...)`. A command that fails
            # inside process substitution does not trip `set -e`, so the API error would
            # be silently indistinguishable from "this account has no clusters" -- the
            # exact silent failure this job exists to avoid.
            if ! clusters_raw=$(aws eks list-clusters --region "$REGION" \
                                  --query 'clusters[]' --output text 2>&1); then
              echo "FATAL: aws eks list-clusters failed: ${clusters_raw}" >&2
              exit 1
            fi
            mapfile -t CLUSTERS < <(printf '%s\n' "$clusters_raw" | tr '\t' '\n' | sed '/^$/d' | sort)

            if [[ ${#CLUSTERS[@]} -eq 0 ]]; then
              echo "FATAL: no EKS clusters visible in ${REGION}" >&2
              exit 1
            fi

            behind=0
            skipped=0
            printf '%-36s %-40s %-22s %-22s %s\n' CLUSTER ADDON INSTALLED DEFAULT STATUS

            for cluster in "${CLUSTERS[@]}"; do
              # The IAM policy scopes DescribeCluster to this stack's clusters, so an
              # unrelated cluster in the account is denied. Warn and continue rather than
              # failing the report -- but warn loudly, so it is never silent.
              if ! k8s=$(aws eks describe-cluster --region "$REGION" --name "$cluster" \
                           --query 'cluster.version' --output text 2>/dev/null); then
                echo "WARN: cannot describe cluster ${cluster}; skipping" >&2
                skipped=$((skipped + 1))
                continue
              fi

              for addon in "${ADDONS[@]}"; do
                installed=$(aws eks describe-addon --region "$REGION" \
                              --cluster-name "$cluster" --addon-name "$addon" \
                              --query 'addon.addonVersion' --output text 2>/dev/null \
                              || echo "NOT_INSTALLED")

                if [[ "$installed" == "NOT_INSTALLED" ]]; then
                  printf '%-36s %-40s %-22s %-22s %s\n' \
                    "$cluster" "$addon" "$installed" "-" "not-installed"
                  continue
                fi

                # defaultVersion, not the newest available: the two diverge, and the
                # default is the version AWS has vetted for that Kubernetes release.
                default=$(aws eks describe-addon-versions --region "$REGION" \
                            --addon-name "$addon" --kubernetes-version "$k8s" \
                            --query 'addons[0].addonVersions[?compatibilities[0].defaultVersion==`true`].addonVersion | [0]' \
                            --output text)

                if [[ "$installed" == "$default" ]]; then
                  status="current"
                else
                  status="BEHIND -> would bump to ${default}"
                  behind=$((behind + 1))
                fi

                printf '%-36s %-40s %-22s %-22s %s\n' \
                  "$cluster" "$addon" "$installed" "$default" "$status"
              done
            done

            echo
            echo "clusters skipped (not readable): ${skipped}"
            echo "addons behind the default version: ${behind}"
            echo
            echo "This job reports only. Nothing was changed."
            # Exits 0 even when something is behind: a red periodic for an expected
            # condition trains people to ignore it.
            exit 0
