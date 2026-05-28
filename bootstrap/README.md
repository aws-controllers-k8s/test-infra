# Cluster Bootstrap (Terraform)

## Prerequisites

- Terraform >= 1.5
- AWS CLI configured with admin permissions
- Docker installed (for building the bootstrap image)
- `kubectl` installed
- `helm` installed
- `yq` installed
- Access to `public.ecr.aws` (for pushing/pulling images)

## First-Time Bootstrap

### 1. Create the Terraform state backend

```bash
cd test-infra/bootstrap
./scripts/bootstrap-backend.sh
```

### 2. Vendor the Flux chart

```bash
cd test-infra
./scripts/pull-flux-chart.sh
git add charts/flux2-*/
git commit -m "chore(flux): vendor flux2 chart"
git push
```

### 3. Create required AWS Secrets Manager secrets

Prow requires the following secrets to exist before deployment:

```bash
# GitHub App credentials (JSON with cert and appid fields)
aws secretsmanager create-secret \
  --name "ack/prow/github-token" \
  --secret-string '{"cert":"<PEM_PRIVATE_KEY>","appid":"<APP_ID>"}'

# GitHub webhook HMAC token
aws secretsmanager create-secret \
  --name "ack/prow/hmac-token" \
  --secret-string "<HMAC_SECRET>"

# GitHub Personal Access Token (for PR operations)
aws secretsmanager create-secret \
  --name "ack/prow/github-pat-token" \
  --secret-string "<PAT_TOKEN>"

# API Model Knowledge Base ID (used by the add-resource workflow agent)
aws secretsmanager create-secret \
  --name "ack/prow/api-model-kb" \
  --secret-string "<KNOWLEDGE_BASE_ID>"

# ECR pull-through cache credentials for ghcr.io/fluxcd
aws secretsmanager create-secret \
  --name "ecr-pullthroughcache/ghcr-fluxcd" \
  --secret-string '{"username":"<GITHUB_USER>","accessToken":"<GITHUB_PAT>"}'

# ECR needs permission to read the pull-through cache secret
aws secretsmanager put-resource-policy \
  --secret-id "ecr-pullthroughcache/ghcr-fluxcd" \
  --resource-policy '{"Version":"2012-10-17","Statement":[{"Sid":"AllowECRAccess","Effect":"Allow","Principal":{"Service":"ecr.amazonaws.com"},"Action":[ "secretsmanager:GetSecretValue", "secretsmanager:BatchGetSecretValue" ],"Resource":"*"}]}'
```

### 4. Generate your environment

```bash
cd test-infra/bootstrap
./scripts/bootstrap-env.sh
```

The script prompts for the deployment stage first, then for each variable
(region, account ID, flux version, GitHub org/repo/branch, domain, etc.) with
smart defaults based on the stage. It stores the config as a SecureString in
SSM at `/ack/test-infra/bootstrap/env/<stage>` and writes a local `.tfvars`
file to `bootstrap/environment/<stage>.tfvars`.

On subsequent runs (or on a fresh clone), it pulls the config from SSM without
prompting. Use `--force` to re-prompt and overwrite.

```bash
# Re-generate from SSM (no prompts — just select stage)
./scripts/bootstrap-env.sh

# Force re-prompt and overwrite SSM
./scripts/bootstrap-env.sh --force
```

> **Note:** Environment files (`bootstrap/environment/*.tfvars`) are gitignored
> and must never be committed. The source of truth is SSM.

### 5. Bootstrap the cluster

```bash
cd test-infra/bootstrap
terraform init
terraform apply -var-file=environment/<stage>.tfvars
```

### 6. Create ACM certificate for Prow domain

The ALB requires a TLS certificate matching the Prow domain. Run this after
the `ack-prow` kustomization is Ready (it creates the Route53 hosted zone):

```bash
./scripts/setup-acm-cert.sh <prow-domain> us-west-2
# e.g. ./scripts/setup-acm-cert.sh gustidia.people.aws.dev us-west-2
```

The script is idempotent — re-running skips already-issued certs. The ALB
auto-discovers the certificate by matching the ingress host.

### 7. Configure GitHub webhook

After Prow is deployed, configure the GitHub App/org webhook to use the Prow
domain (set via the `prow_domain` variable):

- **URL:** `https://<prow_domain>/hook` (e.g., `https://prow.ack.aws.dev/hook`)
- **Content type:** `application/json`
- **Secret:** same value used in `ack/prow/hmac-token` secret


## Upgrading Flux

```bash
# 1. Update the version
yq -i '.version = "2.9.0"' flux/flux-version.yaml

# 2. Vendor the new chart
./scripts/pull-flux-chart.sh

# 3. Commit and push
git add charts/ flux/
git commit -m "chore(flux): upgrade to 2.9.0"
git push
# Flux self-upgrades on next reconciliation
```

## Upgrading Prow

Prow images are mirrored from upstream into a private ECR registry. The version
is controlled by a single ConfigMap (`flux/prow/version/prow-version-configmap.yaml`).

```bash
# Auto-detect latest tags for both Prow core and tools, update CRD
./scripts/upgrade-prow.sh

# Or specify a Prow core tag explicitly
./scripts/upgrade-prow.sh v20260519-c47e31ece

# Preview changes without modifying files
./scripts/upgrade-prow.sh --dry-run

# Commit and push
git add flux/prow/ prow/config/
git commit -m "chore(prow): upgrade to <tag>"
git push
# Flux reconciles: mirror job copies new images to ECR, then Prow redeploys
```

Image sources:
- Prow core: `us-docker.pkg.dev/k8s-infra-prow/images` (13 images)
- Tools (`label_sync`, `commenter`): `gcr.io/k8s-staging-test-infra`

## Re-running Terraform

```bash
# Regenerate local .tfvars from SSM (if on a fresh clone)
./scripts/bootstrap-env.sh

terraform apply -var-file=environment/<stage>.tfvars
```

## Day-2 Infrastructure Changes (ACK Manifests)

After initial bootstrap, all infrastructure changes go through ACK manifests
in `flux/ack/` — not Terraform. Terraform only handles the initial cluster
creation; ACK manages the cluster's desired state going forward.

See [`flux/ack/README.md`](../flux/ack/README.md) for full guidance on:

- Adding and configuring AWS resources (addons, IAM roles, S3, Route53, ECR)
- Variable substitution patterns
- Forcing reconciliation

Workflow:

```bash
# 1. Add or edit ACK manifests in flux/ack/
# 2. Register new files in the appropriate kustomization.yaml
# 3. Ensure the capability role has permissions for the resource type
# 4. Commit and push
# 5. Force reconcile:
kubectl annotate gitrepository test-infra -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
kubectl annotate kustomization <name> -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
```

## Forcing Code Sync from GitHub

Flux polls the GitRepository on its configured interval (typically 60m).
To force an immediate sync after pushing changes:

```bash
# 1. Force Flux to pull the latest commit
kubectl annotate gitrepository test-infra -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite

# 2. Wait for the source to be ready
kubectl wait gitrepository/test-infra -n flux-system \
  --for=condition=Ready --timeout=60s

# 3. Trigger the relevant kustomization(s)
kubectl annotate kustomization <name> -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
```

Common kustomization names:

| Name | What it deploys |
|------|-----------------|
| `flux` | Flux self-management (Helm chart) |
| `ack-capability-role` | ACK capability IAM role |
| `ack-capability` | ACK EKS capability |
| `ack-cluster` | Cluster config, access entries, nodepool |
| `ack-addons-roles` | IAM roles for EKS addons |
| `ack-addons` | EKS managed addons |
| `ack-pod-identity-roles` | IAM roles for pod identities |
| `ack-pod-identities` | Pod identity associations |
| `ack-prow` | Prow AWS resources (S3, Route53) |
| `ack-flux` | ECR pull-through cache |
| `prow-crds` | Prow CRDs |
| `prow-charts` | Prow Helm releases |

If a kustomization shows `dependency '<name>' is not ready`, trigger the
dependency first and work up the chain.

## Tearing Down

```bash
cd test-infra/bootstrap
terraform destroy
```

## Cleaning Up the State Backend

After destroying all infrastructure, you can remove the Terraform state backend.
This is irreversible — only do this if you're fully decommissioning the environment.

```bash
cd test-infra/bootstrap
./scripts/cleanup-backend.sh
```
