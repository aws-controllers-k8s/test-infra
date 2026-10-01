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
      # Reuses the integration-test image for its AWS CLI, the same precedent as
      # upgrade-eks-distro-version. Needs no kubectl and no cluster credentials.
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

            # Literal, not ${STACK_NAME}: only the tokens in the envsubst allow-list in
            # templates/job-config-job.yaml.tpl are resolved, and an unlisted one reaches
            # bash as a literal that expands to empty.
            STACK_NAME="ack-test-infra-prod"

            # The clusters and addons this stack declares Addon CRs for, in
            # flux/ack/charts/{ack-addons,ack-build-infra}. Edited alongside those charts.
            CLUSTERS=("${STACK_NAME}-cluster" "${STACK_NAME}-build-cluster")
            ADDONS=("aws-secrets-store-csi-driver-provider")

            behind=0
            unreadable=0
            printf '%-36s %-40s %-22s %-22s %s\n' CLUSTER ADDON INSTALLED DEFAULT STATUS

            for cluster in "${CLUSTERS[@]}"; do
              # Named on purpose, so a failure is a real fault, not the expected denial of a
              # discovered list. Report the rest, then fail at the end.
              if ! k8s=$(aws eks describe-cluster --region "$REGION" --name "$cluster" \
                           --query 'cluster.version' --output text 2>&1); then
                echo "ERROR: cannot describe cluster ${cluster}: ${k8s}" >&2
                unreadable=$((unreadable + 1))
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
            echo "clusters reported: $(( ${#CLUSTERS[@]} - unreadable )) of ${#CLUSTERS[@]}"
            echo "addons behind the default version: ${behind}"
            echo
            echo "This job reports only. Nothing was changed."

            # Red only for an incomplete report. Being behind is expected, and a red
            # periodic for an expected condition trains people to ignore it.
            if [[ $unreadable -gt 0 ]]; then
              echo "FAILED: ${unreadable} of ${#CLUSTERS[@]} named clusters could not be read." >&2
              exit 1
            fi
            exit 0
