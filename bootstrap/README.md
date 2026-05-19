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
```

### 4. Generate your environment

```bash
cd test-infra/bootstrap
./gen-env
```

This prompts for each variable (region, flux version, GitHub org/repo/branch)
and writes a `.tfvars` file to `bootstrap/environment/dev.tfvars`.

### 5. Bootstrap the cluster

```bash
cd test-infra/bootstrap
terraform init
terraform apply -var-file=environment/dev.tfvars
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

## Re-running Terraform

```bash
terraform apply -var-file=environment/dev.tfvars
```

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
