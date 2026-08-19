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

### 2. Create required AWS Secrets Manager secrets

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
# NOTE: no longer required -- the cache rule served Flux's own images and went with Flux.
aws secretsmanager create-secret \
  --name "ecr-pullthroughcache/ghcr-fluxcd" \
  --secret-string '{"username":"<GITHUB_USER>","accessToken":"<GITHUB_PAT>"}'

# ECR needs permission to read the pull-through cache secret
aws secretsmanager put-resource-policy \
  --secret-id "ecr-pullthroughcache/ghcr-fluxcd" \
  --resource-policy '{"Version":"2012-10-17","Statement":[{"Sid":"AllowECRAccess","Effect":"Allow","Principal":{"Service":"ecr.amazonaws.com"},"Action":[ "secretsmanager:GetSecretValue", "secretsmanager:BatchGetSecretValue" ],"Resource":"*"}]}'
```

### 3. Generate your environment

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

### 4. Bootstrap the cluster

```bash
cd test-infra/bootstrap
terraform init
terraform apply -var-file=environment/<stage>.tfvars
```

### 5. Create ACM certificate for Prow domain

The ALB requires a TLS certificate matching the Prow domain. Run this after
the `ack-prow` kustomization is Ready (it creates the Route53 hosted zone):

```bash
./scripts/setup-acm-cert.sh <prow-domain> us-west-2
# e.g. ./scripts/setup-acm-cert.sh gustidia.people.aws.dev us-west-2
```

The script is idempotent — re-running skips already-issued certs. The ALB
auto-discovers the certificate by matching the ingress host.

### 6. Configure GitHub webhook

After Prow is deployed, configure the GitHub App/org webhook to use the Prow
domain (set via the `prow_domain` variable):

- **URL:** `https://<prow_domain>/hook` (e.g., `https://prow.ack.aws.dev/hook`)
- **Content type:** `application/json`
- **Secret:** same value used in `ack/prow/hmac-token` secret


## Upgrading Argo CD

The Argo CD control plane is an EKS **capability**, managed by AWS off-cluster, so there is no
chart to vendor and nothing in this repo pins its version:

```bash
aws eks describe-capability --cluster-name <cluster> --capability-name argocd --region <region>
```

Flux used to be vendored here as an extracted chart under `charts/` with
`scripts/pull-flux-chart.sh` to refresh it, because Flux had to upgrade itself from a source it
could read. Both are gone -- see `docs/argocd-migration.md`.

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
(`prow/config/values.yaml`, `prow/jobs/values.yaml` and `flux/prow/charts/prow-mirror/values.yaml`).

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
git add flux/prow/charts/ prow/config/
git commit -m "chore(prow): upgrade to <tag>"
git push
# Argo CD syncs: the mirror job rebuilds patched images into ECR, then Prow redeploys
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
`Force=true,Replace=true`), so committing a tag before the image exists makes Argo CD recreate
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
- **`flux/prow/charts/prow-build-cluster-connection/templates/job.yaml`** pins a `prow-kubectl` tag
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
in `flux/ack/charts/` — not Terraform. Terraform only handles the initial cluster
creation; ACK manages the cluster's desired state going forward.

See [`flux/ack/README.md`](../flux/ack/README.md) for full guidance on:

- Adding and configuring AWS resources (addons, IAM roles, S3, Route53, ECR)
- Variable substitution patterns
- Forcing reconciliation

Workflow:

```bash
# 1. Add or edit ACK manifests in the relevant chart under flux/ack/charts/
# 2. Ensure the capability role has permissions for the resource type
# 3. Commit and push -- Argo CD syncs automatically
# 4. To force it immediately:
kubectl annotate applications.argoproj.io <app> -n argocd \
  argocd.argoproj.io/refresh=hard --overwrite
```

## Forcing a Sync from GitHub

Argo CD polls git and syncs automatically -- every Application has `automated` on. To force an
immediate re-evaluation after pushing:

```bash
kubectl annotate applications.argoproj.io <app> -n argocd \
  argocd.argoproj.io/refresh=hard --overwrite

kubectl -n argocd get applications.argoproj.io \
  -o custom-columns='NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status'
```

Two things worth knowing:

- **Refreshing the root does not refresh its children.** `root-applications` renders the other
  Applications; refreshing it re-renders that list, not each child's own source. Refresh the
  Application that owns the path you changed.
- **`automated` does not retry a revision it has already reported on.** A path that failed once
  sits there looking stuck until a hard refresh or a new commit.

Application names match the paths in `argocd/applications/values.yaml`.

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
