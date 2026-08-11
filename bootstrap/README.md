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

Prow images are mirrored from upstream into a private ECR registry **and rebuilt
with current OS packages** on the way through. Upstream Prow pins its ko base
images in `.ko.yaml` and only refreshes that pin every few months, so even the
newest Prow release ships stale `expat`/`openssl`/`curl`. The `prow-mirror` Job
runs `apk upgrade` over each image and publishes it as
`<upstream-tag>-ack.<PROW_PATCH_REVISION>`, plus the bare `<upstream-tag>` as a
compatibility alias carrying the same patched content. The suffixed tag is what
`prow-config` pins, so bumping the revision is what triggers a rollout; the bare
alias means a reference that has not moved to the suffixed tag still gets patched
packages rather than silently staying vulnerable.

Both the version and the patch revision live in a single ConfigMap
(`flux/prow/version/prow-version-configmap.yaml`).

```bash
# Auto-detect latest tags for both Prow core and tools, update CRD
./scripts/upgrade-prow.sh

# Or specify a Prow core tag explicitly
./scripts/upgrade-prow.sh v20260519-c47e31ece

# Preview changes without modifying files
./scripts/upgrade-prow.sh --dry-run

# Re-patch the CURRENT Prow version against newly published OS security
# updates, without moving to a new upstream release. Increments
# PROW_PATCH_REVISION, which is what makes the mirror Job rebuild.
./scripts/upgrade-prow.sh --bump-patch

# Commit and push
git add flux/prow/ prow/config/
git commit -m "chore(prow): upgrade to <tag>"
git push
# Flux reconciles: mirror job rebuilds patched images into ECR, then Prow redeploys
```

`PROW_PATCH_REVISION` is a monotonic counter and is never reset. `PROW_VERSION`
and `TOOLS_VERSION` move independently but share the one suffix, so resetting it
on a version change would repoint the untouched tag at a revision that already
exists in ECR — the mirror would skip the rebuild and that component would
silently roll back to its least-patched build.

Image sources:
- Prow core: `us-docker.pkg.dev/k8s-infra-prow/images` (13 images)
- Tools (`label_sync`, `commenter`): `gcr.io/k8s-staging-test-infra`

The mirror Job refuses to publish an image whose base has no `apk`, rather than
silently shipping it unpatched. If upstream moves a component to a distroless
base, that component will fail loudly and needs a different patch strategy.

### Reacting to an image vulnerability report

**Mirrored Prow components** (`.../prow/<component>`) are almost always outdated
OS packages in the upstream base image, not a Prow defect. Bumping the Prow
version alone usually does **not** fix them. Run
`./scripts/upgrade-prow.sh --bump-patch`, push, then force-reconcile
`prow-mirror`.

**Our own job images** (`public.ecr.aws/<alias>/...-prow-images:*`) apply OS
updates at build time, so they only need a tag bump to be rebuilt. Note the
two-phase flow — do not shortcut it:

1. In your PR, bump the tag in `prow/jobs/images_config.yaml` (or the
   `prow/plugins/` / `prow/agent-workflows/` equivalent) and **nothing else**.
   `compareImageVersions` only builds a tag strictly greater than what is in
   ECR, so an unchanged tag is never rebuilt.
2. On merge, the `build-prow-images` postsubmit builds and pushes the new tags.
3. A bot PR then commits the regenerated `jobs.yaml` / `job-config-job.yaml` /
   `agent-workflows.yaml` / `prow/plugins/deployments/`.

Do not run `make prow-gen` and commit its output in step 1. `job-config-job.yaml`
is applied directly by the `prow-jobs` Kustomization (`interval: 5m`,
`force: true`), so committing a tag before the image exists makes Flux recreate
`job-config-substitutor` on a missing image within minutes of merge, halting
`job-config` refreshes until the postsubmit lands.

#### Exceptions the generator does not cover

- **`build-prow-images` is safe to bump on its own.** It is the image the
  postsubmit itself runs as, but the running pod resolves it from the
  already-applied `job-config` ConfigMap, not from `images_config.yaml`. So the
  job builds its own successor. This only holds while step 1 above is respected;
  hand-committing a regenerated `jobs.yaml` would point the job at the image it
  has not built yet.
- **Changing `prow/jobs/tools/` requires bumping `build-prow-images`.**
  `ack-build-tools` is compiled into that image, so without a bump the new binary
  is never built.
- **`flux/prow/build-cluster-connection/job.yaml`** pins a `prow-kubectl` tag
  but is not in the generator's output set, so `make prow-gen` will never update
  it. Bump it by hand once the new tag is in ECR, or its OS packages silently
  stay behind.

Keep notes like these here rather than as comments in `images_config.yaml`: the
`upgrade-go-version` periodic rewrites that file through a typed struct and a
YAML encoder, which strips comments.

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
| `prow-version` | `prow-version` ConfigMap (Prow/tools versions + patch revision) |
| `prow-mirror` | Job that rebuilds upstream Prow images with current OS packages |
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
