# Prow Migration Plan: Full Upgrade to Latest Official Images

## Current State

- **Images**: `public.ecr.aws/eks-distro-build-tooling/prow-*:v20260316-26fa34da6`
- **Target**: `us-docker.pkg.dev/k8s-infra-prow/images/*:v20260519-c47e31ece`
- **CRD**: `controller-gen v0.6.3` → `v0.17.3`
- **Config format**: Uses `default_decoration_configs` (map format, deprecated April 2020 but still supported)

---

## Required Changes

### 1. CRD Upgrade (Low Risk)

**File**: `flux/prow/crds/prowjob_customresourcedefinition.yaml`

**Change**: Replace with upstream CRD from `kubernetes-sigs/prow@main`

**Key differences**:
- `controller-gen v0.6.3` → `v0.17.3` (reformatted descriptions, no schema breaks)
- Added `preserveUnknownFields: false` (stricter validation)
- Added new optional fields: `blobless_fetch`, additional decoration options
- No removed fields — fully backwards compatible

**Risk**: The `preserveUnknownFields: false` addition means any ProwJobs with
typos or unknown fields will now be rejected. This is desirable but could
surface latent issues.

**Action**: Apply with `kubectl apply --server-side=true` (CRD is too large for
client-side apply). The Flux `prow-crds` Kustomization already handles this.

---

### 2. Image Registry Migration (Medium Risk)

**File**: `prow/config/values.yaml`

**Changes**: All 13 image references updated (already done by `upgrade-prow.sh`).

**Image name differences**:

| EKS Distro | Official | Notes |
|---|---|---|
| `prow-crier` | `crier` | Name change |
| `prow-deck` | `deck` | Name change |
| `prow-ghproxy` | `ghproxy` | Name change |
| `prow-hook` | `hook` | Name change |
| `prow-horologium` | `horologium` | Name change |
| `prow-controller-manager` | `prow-controller-manager` | Same name |
| `prow-sinker` | `sinker` | Name change |
| `prow-statusreconciler` | `statusreconciler` | Name change |
| `prow-tide` | `tide` | Name change |
| `prow-clonerefs` | `clonerefs` | Name change |
| `prow-entrypoint` | `entrypoint` | Name change |
| `prow-initupload` | `initupload` | Name change |
| `prow-sidecar` | `sidecar` | Name change |

**Binary path change (CRITICAL)**: Since `v20220222-acb5731b85`, official Prow
images are built with `ko`. Binaries live at `/ko-app/<name>` (also on `$PATH`).
EKS Distro images may have binaries at different paths. This affects:
- The `commenter` image used in periodic jobs (`/app/robots/commenter/app.binary`
  → `/ko-app/commenter`)
- The `label_sync` image used in periodic jobs

---

### 3. Stale Job Images in `jobs.yaml` (HIGH PRIORITY)

**File**: `prow/jobs/jobs.yaml` (auto-generated)

Several periodic jobs reference **very old** images from the frozen `gcr.io/k8s-prow`:

| Job | Current Image | Issue |
|-----|---------------|-------|
| `label-sync` | `gcr.io/k8s-prow/label_sync:v20221205-a1b0b85d88` | 3+ years old, frozen registry |
| `periodic-close` | `gcr.io/k8s-prow/commenter:v20210422-d12e80af3e` | 5+ years old, uses old binary path |
| `periodic-rotten` | `gcr.io/k8s-prow/commenter:v20210422-d12e80af3e` | Same as above |

**Required changes**:

```yaml
# label-sync: update image and verify command still works
# Old:
image: gcr.io/k8s-prow/label_sync:v20221205-a1b0b85d88
command: [label_sync]

# New:
image: us-docker.pkg.dev/k8s-infra-prow/images/label_sync:v20260519-c47e31ece
command: [label_sync]  # Still works (ko puts binaries on $PATH)
```

```yaml
# commenter: update image AND fix binary path
# Old:
image: gcr.io/k8s-prow/commenter:v20210422-d12e80af3e
command: [/app/robots/commenter/app.binary]

# New:
image: us-docker.pkg.dev/k8s-infra-prow/images/commenter:v20260519-c47e31ece
command: [commenter]  # ko-built binary is on $PATH
```

**Where to fix**: These jobs are defined directly in `prow/jobs/jobs.yaml`
(not generated from templates). Update the image references and commands there,
or move them into the template system.

---

### 4. Config Format: `default_decoration_configs` (Low Risk)

**File**: `prow/config/templates/config-ConfigMap.yaml`

**Current format** (map-based, introduced Oct 2019):
```yaml
plank:
  default_decoration_configs:
    "*":
      timeout: 48h
      utility_images: ...
```

**Recommended format** (slice-based, introduced March 2021):
```yaml
plank:
  default_decoration_config_entries:
  - config:
      timeout: 48h
      utility_images: ...
```

**Status**: The old `default_decoration_configs` map format is **still supported**
and was never formally deprecated (per the March 2021 announcement: "The old
field is still supported and will not be deprecated"). However, the upstream
starter manifests now use `default_decoration_config_entries`.

**Recommendation**: Migrate to `default_decoration_config_entries` for
consistency with upstream, but this is **not blocking** for the image upgrade.

**Additional upstream fields to consider adding**:
```yaml
default_decoration_config_entries:
- config:
    github_api_endpoints:
      - http://ghproxy
      - https://api.github.com
    github_app_id: "$(GITHUB_APP_ID)"
    github_app_private_key_secret:
      name: github-token
      key: cert
```

These fields allow decoration config to handle GitHub auth for clonerefs
directly, rather than relying on preset environment variables.

---

### 5. Deployment Template Flags (No Changes Needed)

All container args in the Helm templates are compatible with the latest Prow:

| Component | Flags | Status |
|-----------|-------|--------|
| `prow-controller-manager` | `--enable-controller=plank` | ✅ Still valid |
| `hook` | `--github-app-id`, `--github-app-private-key-path` | ✅ Current |
| `tide` | `--status-path=s3://`, `--history-uri=s3://` | ✅ S3 still supported |
| `deck` | `--spyglass=true` | ✅ Still valid |
| `crier` | `--blob-storage-workers` | ✅ Current (old `--gcs-workers` removed May 2022) |
| `sinker` | `--config-path`, `--job-config-path` | ✅ Current |
| `statusreconciler` | `--status-path=s3://` | ✅ Still valid |
| `ghproxy` | `--cache-dir`, `--cache-sizeGB`, `--serve-metrics` | ✅ Current |

No deprecated flags detected in the current deployment templates.

---

### 6. RBAC Changes (Low Risk)

**File**: `prow/data-plane/templates/prow-controller-manager-Role.yaml`

Current RBAC for prow-controller-manager in the test-pods namespace:
```yaml
rules:
- apiGroups: [""]
  resources: [pods]
  verbs: [delete, list, watch, create, patch]
```

**Potential addition needed**: Newer Prow versions may require `get` on pods.
Verify after deployment — if prow-controller-manager logs show RBAC errors,
add `get` to the verbs list.

---

### 7. Plugins Configuration (No Changes Needed)

The plugins config uses standard plugins that are all still supported:
`approve`, `assign`, `blunderbuss`, `help`, `hold`, `label`, `lgtm`,
`lifecycle`, `trigger`, `verify-owners`, `wip`.

The external plugin `agent-plugin` is custom and unaffected by the Prow upgrade.

---

### 8. Entrypoint Compatibility (CRITICAL)

**Breaking change from Feb 2022**: Since `v20220222-acb5731b85`, the entrypoint
container uses `--copy-mode-only` instead of `/bin/cp /entrypoint /tools/entrypoint`.

**Impact**: Pod utility images (clonerefs, entrypoint, initupload, sidecar) must
ALL be from the same version. Mixing old utility images with new Prow components
(or vice versa) will break job execution.

**Current state**: All utility images are already set to the same tag in
`values.yaml`, so this is handled correctly. Just ensure no job overrides
`utility_images` with older versions.

---

## Migration Checklist

### Phase 1: Pre-flight (no cluster changes)

- [ ] Run `./scripts/upgrade-prow.sh` to update images and CRD
- [ ] Update `label-sync` job image → `us-docker.pkg.dev/k8s-infra-prow/images/label_sync:<tag>`
- [ ] Update `commenter` jobs image → `us-docker.pkg.dev/k8s-infra-prow/images/commenter:<tag>`
- [ ] Fix `commenter` command: `/app/robots/commenter/app.binary` → `commenter`
- [ ] Verify no other jobs reference `gcr.io/k8s-prow/` directly
- [ ] Bump `prow/config/Chart.yaml` version (done by script)

### Phase 2: Staging deployment

- [ ] Push changes to staging branch
- [ ] Force Flux reconciliation on staging cluster
- [ ] Verify all Prow pods start (check image pull succeeds from `us-docker.pkg.dev`)
- [ ] Verify Deck UI loads at staging domain
- [ ] Trigger a test presubmit job — verify clonerefs/entrypoint/sidecar work
- [ ] Verify `label-sync` periodic runs successfully
- [ ] Verify tide syncs PR status
- [ ] Check prow-controller-manager logs for RBAC errors
- [ ] Monitor for 24h

### Phase 3: Production deployment

- [ ] Merge to main
- [ ] Force Flux reconciliation
- [ ] Monitor webhook delivery (hook pod logs)
- [ ] Verify first real presubmit/postsubmit completes
- [ ] Monitor for 48h

### Phase 4: Cleanup (post-migration)

- [ ] (Optional) Migrate `default_decoration_configs` → `default_decoration_config_entries`
- [ ] (Optional) Add `github_app_id` / `github_app_private_key_secret` to decoration config
- [ ] Remove any remaining `gcr.io/k8s-prow/` references
- [ ] Update Deck branding (currently references EKS Distro logos/colors)
- [ ] Consider setting up ECR pull-through cache for `us-docker.pkg.dev/k8s-infra-prow`

---

## Deck Branding Update (Optional)

The config currently uses EKS Distro branding:
```yaml
deck:
  branding:
    favicon: 'https://distro.eks.amazonaws.com/assets/images/favicon.ico'
    header_color: '#232F3E'
    logo: 'https://distro.eks.amazonaws.com/assets/images/amazon-eks-distro-white-logo.png'
```

Consider updating to ACK-specific branding since we're removing the EKS Distro
dependency.

---

## Summary of Files to Modify

| File | Change | Priority |
|------|--------|----------|
| `prow/config/values.yaml` | Image registry + tag | ✅ Done by script |
| `prow/config/Chart.yaml` | Version bump | ✅ Done by script |
| `flux/prow/crds/prowjob_customresourcedefinition.yaml` | Latest CRD | ✅ Done by script |
| `prow/jobs/jobs.yaml` | Fix `label_sync` + `commenter` images/commands | **Manual** |
| `prow/config/templates/config-ConfigMap.yaml` | (Optional) `default_decoration_config_entries` | Low priority |
| `prow/config/templates/config-ConfigMap.yaml` | (Optional) Remove EKS Distro branding | Low priority |
