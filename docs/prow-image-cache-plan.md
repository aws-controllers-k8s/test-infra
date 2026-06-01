# Bespoke Pull-Through Cache for Prow Images

## Problem

ECR pull-through cache does not support `us-docker.pkg.dev` (Google Artifact
Registry) as an upstream. The supported upstreams are: Docker Hub, GitHub
Container Registry, Quay, Azure Container Registry, GitLab, and public ECR.

Prow images live at `us-docker.pkg.dev/k8s-infra-prow/images/*` and cannot be
cached natively by ECR.

## Goals

- Avoid direct pulls from `us-docker.pkg.dev` on every pod start (latency, rate limits)
- Mirror Prow images into a local ECR repository
- Keep the mirror updated automatically when we upgrade Prow
- Follow the existing pattern (FluxCD uses ECR pull-through cache via ACK)

---

## Approach: CronJob-Based Image Mirror

Since ECR can't do this natively, we implement a Kubernetes CronJob (or
one-shot Job triggered by Flux) that copies images from the upstream registry
into a private ECR repository using `crane copy`.

### Architecture

```
┌─────────────────────────────┐
│  us-docker.pkg.dev          │
│  /k8s-infra-prow/images     │
│  (upstream, public)         │
└──────────────┬──────────────┘
               │ crane copy (CronJob)
               ▼
┌─────────────────────────────┐
│  <account>.dkr.ecr          │
│  .<region>.amazonaws.com    │
│  /prow/<image>:<tag>        │
│  (private ECR mirror)       │
└──────────────┬──────────────┘
               │ image pull (nodes)
               ▼
┌─────────────────────────────┐
│  EKS Cluster                │
│  (Prow components + jobs)   │
└─────────────────────────────┘
```

### Components

#### 1. ECR Repository (Terraform)

Create a single ECR repository to hold all mirrored Prow images:

```hcl
# bootstrap/ecr.tf (or add to existing)
resource "aws_ecr_repository" "prow_mirror" {
  name                 = "prow"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = false
  }

  lifecycle {
    ignore_changes = all  # ACK manages after bootstrap
  }
}

resource "aws_ecr_lifecycle_policy" "prow_mirror" {
  repository = aws_ecr_repository.prow_mirror.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep last 5 image tags per component"
      selection = {
        tagStatus   = "tagged"
        tagPrefixList = ["v"]
        countType   = "imageCountMoreThan"
        countNumber = 50  # ~5 tags × 13 components
      }
      action = { type = "expire" }
    }]
  })
}
```

#### 2. Node IAM Permissions (Terraform)

Add ECR pull permissions for the `prow/*` prefix:

```hcl
# Add to the existing node_ecr_ptc policy
resource "aws_iam_role_policy" "node_ecr_ptc" {
  # ... existing policy ...
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # Existing: FluxCD pull-through cache
        Effect   = "Allow"
        Action   = ["ecr:CreateRepository", "ecr:BatchImportUpstreamImage"]
        Resource = ["arn:${local.partition}:ecr:${var.region}:${local.account_id}:repository/fluxcd/*"]
      },
      {
        # New: Prow mirror read access (nodes just need to pull)
        Effect   = "Allow"
        Action   = ["ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"]
        Resource = ["arn:${local.partition}:ecr:${var.region}:${local.account_id}:repository/prow/*"]
      }
    ]
  })
}
```

#### 3. Mirror Job (Flux Kustomization)

A Kubernetes Job that runs `crane copy` for each Prow image. Triggered by Flux
on every reconciliation (using `force: true` like the existing `prow-build-images` Job).

```yaml
# flux/prow/mirror/mirror-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: prow-mirror-images
  namespace: test-pods
spec:
  ttlSecondsAfterFinished: 86400
  backoffLimit: 2
  template:
    spec:
      serviceAccountName: post-submit-service-account
      restartPolicy: Never
      containers:
      - name: mirror
        image: gcr.io/go-containerregistry/crane:latest
        command:
        - /bin/sh
        - -c
        - |
          set -eu

          SRC_REGISTRY="us-docker.pkg.dev/k8s-infra-prow/images"
          DST_REGISTRY="${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com/prow"
          TAG="${PROW_VERSION}"

          # Login to ECR
          aws ecr get-login-password --region ${REGION} | \
            crane auth login "${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com" \
              --username AWS --password-stdin

          # List of Prow images to mirror
          IMAGES="
            crier
            deck
            ghproxy
            hook
            horologium
            prow-controller-manager
            sinker
            statusreconciler
            tide
            clonerefs
            entrypoint
            initupload
            sidecar
            label_sync
            commenter
          "

          for img in $IMAGES; do
            echo "Copying ${SRC_REGISTRY}/${img}:${TAG} → ${DST_REGISTRY}/${img}:${TAG}"
            crane copy "${SRC_REGISTRY}/${img}:${TAG}" "${DST_REGISTRY}/${img}:${TAG}" || \
              echo "  WARNING: failed to copy ${img}, continuing..."
          done

          echo "Mirror complete."
        env:
        - name: ACCOUNT_ID
          value: "${ACCOUNT_ID}"
        - name: REGION
          value: "${REGION}"
        - name: PROW_VERSION
          value: "${PROW_VERSION}"
        - name: AWS_REGION
          value: "${REGION}"
        resources:
          requests:
            cpu: "500m"
            memory: "256Mi"
          limits:
            cpu: "1"
            memory: "512Mi"
```

#### 4. Flux Kustomization

```yaml
# Add to flux/prow.yaml
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: prow-mirror
  namespace: flux-system
spec:
  interval: 60m
  sourceRef:
    kind: GitRepository
    name: test-infra
  path: ./flux/prow/mirror
  prune: false
  force: true  # Recreate Job on each reconciliation
  postBuild:
    substituteFrom:
    - kind: ConfigMap
      name: self-managed-vars
    - kind: ConfigMap
      name: prow-version
  dependsOn:
  - name: ack-pod-identities
```

#### 5. Version ConfigMap

Track the Prow version for substitution:

```yaml
# flux/prow/prow-version-configmap.yaml (deployed by Terraform or Flux)
apiVersion: v1
kind: ConfigMap
metadata:
  name: prow-version
  namespace: flux-system
data:
  PROW_VERSION: "v20260519-c47e31ece"
```

#### 6. Update values.yaml to Use ECR Mirror

Once the mirror is in place, update `prow/config/values.yaml` to reference
the ECR mirror instead of the upstream registry:

```yaml
# Before (direct upstream)
crier:
  image: us-docker.pkg.dev/k8s-infra-prow/images/crier:v20260519-c47e31ece

# After (ECR mirror)
crier:
  image: ${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com/prow/crier:v20260519-c47e31ece
```

This can be templated using Flux variable substitution by making the registry
a variable:

```yaml
# values.yaml uses a variable
crier:
  image: ${PROW_IMAGE_REGISTRY}/crier:${PROW_VERSION}
```

With `PROW_IMAGE_REGISTRY` set in the `self-managed-vars` ConfigMap:
```
PROW_IMAGE_REGISTRY = "${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com/prow"
```

---

## Upgrade Workflow (After Implementation)

```bash
# 1. Run upgrade script (updates templates + jobs.yaml with new tag)
./scripts/upgrade-prow.sh

# 2. Update the prow-version ConfigMap (or have the script do it)
yq -i '.data.PROW_VERSION = "v20260519-newversion"' flux/prow/prow-version-configmap.yaml

# 3. Commit and push
git add -A && git commit -m "chore(prow): upgrade to <tag>"
git push

# 4. Flux reconciles:
#    a. prow-mirror Job runs → copies new images to ECR
#    b. prow-charts reconciles → deploys new Prow with ECR image refs
```

---

## Ordering / Dependencies

```
prow-mirror (copies images to ECR)
    ↓
prow-charts (deploys Prow using ECR images)
```

The `prow-charts` Kustomization should `dependsOn: prow-mirror` to ensure
images are available before Prow pods try to pull them.

---

## Alternative Approaches Considered

### A. Skopeo CronJob (periodic sync)
- Runs on a schedule (e.g., every 6h) regardless of upgrades
- Pro: Simpler, no dependency chain
- Con: Wastes resources when nothing changed; delay between push and availability

### B. GitHub Actions mirror (external to cluster)
- A GitHub Actions workflow copies images on PR merge
- Pro: No in-cluster job needed
- Con: Requires cross-account ECR push credentials in GitHub; adds external dependency

### C. ECR Replication from another account
- Mirror into a GCR-compatible account first, then replicate
- Con: Overly complex, no real benefit

### D. Just pull directly from upstream (no cache)
- Pro: Zero infrastructure
- Con: Subject to rate limits, cross-region latency, upstream outages

**Recommendation**: The Job-based approach (option in this plan) is the best fit
because it:
- Follows the existing `prow-build-images` pattern already in the cluster
- Only runs when Flux reconciles (i.e., when code changes)
- Integrates cleanly with the Flux dependency chain
- Uses pod identity for ECR auth (no extra credentials)

---

## Implementation Checklist

- [ ] Create ECR repository `prow` (Terraform or ACK manifest)
- [ ] Add node IAM permissions for `prow/*` ECR repos
- [ ] Create `flux/prow/mirror/` directory with mirror Job + kustomization.yaml
- [ ] Create `prow-version` ConfigMap (or add `PROW_VERSION` to `self-managed-vars`)
- [ ] Add `prow-mirror` Kustomization to `flux/prow.yaml`
- [ ] Update `prow-charts` to `dependsOn: prow-mirror`
- [ ] Add `PROW_IMAGE_REGISTRY` variable to `self-managed-vars` ConfigMap
- [ ] Refactor `values.yaml` to use `${PROW_IMAGE_REGISTRY}/${component}:${PROW_VERSION}`
- [ ] Update `upgrade-prow.sh` to also bump the version ConfigMap
- [ ] Test on staging
