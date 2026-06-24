# Job that creates the job-config ConfigMap with variable substitution.
#
# Problem: jobs.yaml contains ${TEST_INFRA_ORG}, ${TEST_INFRA_REPO}, and
# ${TEST_INFRA_BRANCH} placeholders. The file exceeds the 1MB ConfigMap limit
# so it must be gzipped. But Flux postBuild substitution cannot operate on
# binaryData fields (gzipped content). This Job bridges the gap by:
#   1. Cloning the repo to get the raw jobs.yaml with placeholders
#   2. Running envsubst to resolve variables from the environment
#   3. Gzipping the substituted content
#   4. Creating/updating the job-config ConfigMap with the gzipped data
#
# The Job runs in the prow namespace where the ConfigMap is consumed.
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: job-config-manager
  namespace: prow
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: job-config-manager
  namespace: prow
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  resourceNames: ["job-config"]
  verbs: ["get", "update", "patch"]
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: job-config-manager
  namespace: prow
subjects:
- kind: ServiceAccount
  name: job-config-manager
  namespace: prow
roleRef:
  kind: Role
  name: job-config-manager
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: batch/v1
kind: Job
metadata:
  name: job-config-substitutor
  namespace: prow
spec:
  ttlSecondsAfterFinished: 300
  backoffLimit: 2
  template:
    spec:
      serviceAccountName: job-config-manager
      restartPolicy: Never
      initContainers:
      - name: clone-repo
        image: alpine/git:latest
        command:
        - /bin/sh
        - -ec
        - |
          git clone --depth 1 --branch "${TEST_INFRA_BRANCH}" \
            "https://github.com/${TEST_INFRA_ORG}/${TEST_INFRA_REPO}.git" /workspace
        env:
        - name: TEST_INFRA_ORG
          value: "${TEST_INFRA_ORG}"
        - name: TEST_INFRA_REPO
          value: "${TEST_INFRA_REPO}"
        - name: TEST_INFRA_BRANCH
          value: "${TEST_INFRA_BRANCH}"
        - name: CONTROLLER_ECR_REGISTRY
          value: "${CONTROLLER_ECR_REGISTRY}"
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      - name: substitute-and-gzip
        image: public.ecr.aws/docker/library/alpine:3.21
        command:
        - /bin/sh
        - -ec
        - |
          apk add --no-cache gettext > /dev/null 2>&1

          echo "Substituting variables in jobs.yaml..."
          # Substitute only the 4 variables needed for Prow job configuration.
          # The $$ prefix is escaped by Flux postBuild (becomes $ after substitution).
          envsubst '$$TEST_INFRA_ORG $$TEST_INFRA_REPO $$TEST_INFRA_BRANCH $$PROW_IMAGES_REPO_URI $$CONTROLLER_ECR_REGISTRY $$PROW_MIRROR_REGISTRY $$PROW_VERSION $$TOOLS_VERSION' \
            < /workspace/prow/jobs/jobs.yaml \
            > /output/jobs-substituted.yaml

          # Verify no unresolved TEST_INFRA variables remain
          if grep -qF 'TEST_INFRA_ORG}' /output/jobs-substituted.yaml || \
             grep -qF 'TEST_INFRA_REPO}' /output/jobs-substituted.yaml || \
             grep -qF 'TEST_INFRA_BRANCH}' /output/jobs-substituted.yaml; then
            echo "ERROR: Variable substitution incomplete!"
            grep -c 'TEST_INFRA' /output/jobs-substituted.yaml || true
            exit 1
          fi

          # Gzip and base64 encode for ConfigMap binaryData
          gzip -c /output/jobs-substituted.yaml > /output/jobs.yaml.gz
          base64 -w0 /output/jobs.yaml.gz > /output/jobs.yaml.gz.b64

          echo "Substitution complete. Gzipped size: $(wc -c < /output/jobs.yaml.gz) bytes"
        env:
        - name: TEST_INFRA_ORG
          value: "${TEST_INFRA_ORG}"
        - name: TEST_INFRA_REPO
          value: "${TEST_INFRA_REPO}"
        - name: TEST_INFRA_BRANCH
          value: "${TEST_INFRA_BRANCH}"
        - name: PROW_IMAGES_REPO_URI
          value: "${PROW_IMAGES_REPO_URI}"
        - name: CONTROLLER_ECR_REGISTRY
          value: "${CONTROLLER_ECR_REGISTRY}"
        - name: PROW_MIRROR_REGISTRY
          value: "${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com/prow"
        - name: PROW_VERSION
          value: "${PROW_VERSION}"
        - name: TOOLS_VERSION
          value: "${TOOLS_VERSION}"
        volumeMounts:
        - name: workspace
          mountPath: /workspace
        - name: output
          mountPath: /output
      containers:
      - name: apply-configmap
        image: {{printf "%s:%s" .ImageContext.ImageRepo (index .ImageContext.Images "kubectl") }}
        command:
        - /bin/bash
        - -ec
        - |
          GZIPPED_B64=$(cat /output/jobs.yaml.gz.b64)

          echo "Creating/updating job-config ConfigMap in prow namespace..."
          cat <<EOF | kubectl apply -f -
          apiVersion: v1
          kind: ConfigMap
          metadata:
            name: job-config
            namespace: prow
          binaryData:
            config.yaml: "$${GZIPPED_B64}"
          EOF

          echo "Done. ConfigMap job-config updated."
          echo "Values: org=${TEST_INFRA_ORG}, repo=${TEST_INFRA_REPO}, branch=${TEST_INFRA_BRANCH}"
        env:
        - name: TEST_INFRA_ORG
          value: "${TEST_INFRA_ORG}"
        - name: TEST_INFRA_REPO
          value: "${TEST_INFRA_REPO}"
        - name: TEST_INFRA_BRANCH
          value: "${TEST_INFRA_BRANCH}"
        - name: CONTROLLER_ECR_REGISTRY
          value: "${CONTROLLER_ECR_REGISTRY}"
        volumeMounts:
        - name: output
          mountPath: /output
        resources:
          requests:
            cpu: "250m"
            memory: "256Mi"
          limits:
            cpu: "500m"
            memory: "512Mi"
      volumes:
      - name: workspace
        emptyDir: {}
      - name: output
        emptyDir: {}
