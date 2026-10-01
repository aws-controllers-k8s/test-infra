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

            # The clusters to report on, named literally. These are the two clusters whose
            # Addon CRs this repo declares -- ack-addons/templates/addons.yaml for the
            # control plane and ack-build-infra/templates/addons.yaml for the build cluster
            # -- so this list and those charts are edited together.
            #
            # Literal rather than substituted: no ${TOKEN} here is resolved unless it is in
            # the envsubst allow-list in templates/job-config-job.yaml.tpl, and putting one
            # there means threading a value through Terraform's chart values, the Argo CD
            # Application, and the prow-jobs chart -- four files and a `terraform apply` to
            # carry two strings that only change if the stack is renamed.
            #
            # Literal rather than discovered with eks:ListClusters, too. Discovery lists the
            # ACCOUNT, not this stack, so it returns clusters Prow does not own, and from
            # inside the job "I may not describe it" and "it is not ours" look identical:
            # the report would either go quiet about a cluster it should cover or go red
            # about one it should not.
            CLUSTERS=(
              "ack-test-infra-prod-cluster"
              "ack-test-infra-prod-build-cluster"
            )

            behind=0
            unreadable=0
            printf '%-36s %-40s %-22s %-22s %s\n' CLUSTER ADDON INSTALLED DEFAULT STATUS

            for cluster in "${CLUSTERS[@]}"; do
              # Every cluster here was named on purpose, so a failure is a real fault --
              # renamed, deleted, or a policy that no longer covers it -- not the expected
              # "not ours" of a discovered list. Keep going so the other clusters are still
              # reported, then exit non-zero at the end: a report that silently covers one of
              # two clusters is the failure this job exists to catch. This is also what
              # catches the list above going stale against a renamed cluster.
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

            # Red only for an incomplete report. Being behind is an expected condition and a
            # red periodic for one trains people to ignore it; a report missing a cluster it
            # was told to cover is not expected and must not pass quietly.
            if [[ $unreadable -gt 0 ]]; then
              echo "FAILED: ${unreadable} of ${#CLUSTERS[@]} named clusters could not be read." >&2
              exit 1
            fi
            exit 0
