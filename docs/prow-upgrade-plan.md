# Prow Upgrade Plan: Migrate to Official Images & Upgrade CRD

## Background

ACK's Prow deployment currently uses **EKS Distro** images from
`public.ecr.aws/eks-distro-build-tooling/prow-*:v20260316-26fa34da6`.

The official Prow project publishes images to `us-docker.pkg.dev/k8s-infra-prow/images/<component>:<tag>`
(e.g., `us-docker.pkg.dev/k8s-infra-prow/images/deck:v20260519-c47e31ece`). These are the canonical
images used by the Kubernetes project itself and are the only images guaranteed
to stay in sync with upstream Prow development.

> Note: The legacy `gcr.io/k8s-prow` registry stopped receiving updates in
> August 2024 when Prow moved to `kubernetes-sigs/prow`. The new registry is
> `us-docker.pkg.dev/k8s-infra-prow/images`.

**Why migrate:**
- EKS Distro Prow images are a downstream rebuild with no guaranteed update cadence
- Official images are what Prow's own autobumper targets
- Reduces supply-chain risk by depending on the canonical source
- Enables use of Prow's `generic-autobumper` tool for automated version bumps

---

## Phase 1: Preparation (Low Risk)

### 1.1 Create `prow/prow-version.yaml` version file

Similar to how Flux versions are tracked in `flux/flux/version-configmap.yaml`,
create a single source of truth for the Prow image tag:

```yaml
# prow/prow-version.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: prow-version
  namespace: flux-system
data:
  PROW_VERSION: "v20260519-c47e31ece"  # latest official tag
  PROW_IMAGE_REGISTRY: "us-docker.pkg.dev/k8s-infra-prow/images"
```

This ConfigMap gets deployed by Terraform (or added to Flux's substituteFrom)
so all Kustomizations can reference `${PROW_VERSION}` and `${PROW_IMAGE_REGISTRY}`.

### 1.2 Create `scripts/upgrade-prow.sh`

A script that:
1. Queries `gcr.io/k8s-prow` for the latest image tag (using `crane` or `gcloud`)
2. Downloads the latest ProwJob CRD from the official repo
3. Updates `prow/prow-version.yaml` with the new tag
4. Updates `flux/prow/crds/prowjob_customresourcedefinition.yaml`
5. Bumps `prow/config/Chart.yaml` version
6. Prints a summary of changes

### 1.3 Create `scripts/upgrade-prow-crd.sh`

A focused script that only handles CRD upgrades:
1. Downloads the CRD from `https://raw.githubusercontent.com/kubernetes-sigs/prow/main/config/prow/cluster/prowjob-crd/prowjob_customresourcedefinition.yaml`
2. Validates the CRD with `kubectl apply --dry-run=client`
3. Replaces `flux/prow/crds/prowjob_customresourcedefinition.yaml`
4. Prints diff

---

## Phase 2: Helm Chart Changes (Medium Risk)

### 2.1 Update `prow/config/values.yaml`

Replace all `public.ecr.aws/eks-distro-build-tooling/prow-*` references with
template variables that resolve from the version ConfigMap:

```yaml
# Before
crier:
  image: public.ecr.aws/eks-distro-build-tooling/prow-crier:v20260316-26fa34da6

# After
crier:
  image: us-docker.pkg.dev/k8s-infra-prow/images/crier:v20260519-c47e31ece
```

All 12 image references need updating:
- `crier`, `deck`, `ghproxy`, `hook`, `horologium`, `prowControllerManager`,
  `sinker`, `statusreconciler`, `tide`
- Utility images: `clonerefs`, `entrypoint`, `initupload`, `sidecar`

**Image name mapping (EKS Distro → Official):**

| EKS Distro | Official |
|------------|----------|
| `prow-crier` | `crier` |
| `prow-deck` | `deck` |
| `prow-ghproxy` | `ghproxy` |
| `prow-hook` | `hook` |
| `prow-horologium` | `horologium` |
| `prow-controller-manager` | `prow-controller-manager` |
| `prow-sinker` | `sinker` |
| `prow-statusreconciler` | `statusreconciler` |
| `prow-tide` | `tide` |
| `prow-clonerefs` | `clonerefs` |
| `prow-entrypoint` | `entrypoint` |
| `prow-initupload` | `initupload` |
| `prow-sidecar` | `sidecar` |

### 2.2 Bump Chart version

Increment `prow/config/Chart.yaml` version to `0.5.0` to signal the registry change.

### 2.3 Update Flux HelmRelease values

In `flux/prow/charts/prow-config.yaml`, the HelmRelease values override
already use `${PROW_IMAGES_REPO_URI}` for job images. The Prow component images
are set via the chart's `values.yaml` defaults, so updating that file is sufficient.

---

## Phase 3: CRD Upgrade (Medium Risk)

### 3.1 Download latest CRD

The official CRD lives at:
```
https://raw.githubusercontent.com/kubernetes-sigs/prow/main/config/prow/cluster/prowjob-crd/prowjob_customresourcedefinition.yaml
```

### 3.2 Validate compatibility

The CRD is additive (new fields, new printer columns). Existing ProwJobs will
continue to work. Key checks:
- `spec.versions[].schema` changes (new optional fields are safe)
- No removed fields
- `storedVersions` still includes `v1`

### 3.3 Deploy via Flux

The CRD is already deployed via the `prow-crds` Kustomization which depends on
`ack-pod-identities`. Replacing the file and pushing triggers automatic deployment.

---

## Phase 4: Rollout Strategy

### 4.1 Test on staging first

```bash
# 1. Run the upgrade script targeting staging
./scripts/upgrade-prow.sh

# 2. Push to staging branch
git add -A && git commit -m "chore(prow): migrate to official images"
git push origin staging-prow-upgrade

# 3. Update staging.tfvars branch reference
# 4. Force reconcile and monitor
kubectl annotate gitrepository test-infra -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
```

### 4.2 Verify on staging

- [ ] All Prow pods start successfully with new images
- [ ] Deck UI loads
- [ ] Hook receives and processes webhooks
- [ ] Tide syncs PR status
- [ ] A test ProwJob runs end-to-end (clonerefs → entrypoint → sidecar upload)
- [ ] CRD upgrade doesn't disrupt running jobs

### 4.3 Production rollout

After staging validation:
1. Merge to main
2. Force reconcile production
3. Monitor for 24h

---

## Phase 5: Ongoing Maintenance

### 5.1 Automated version bumps (future)

Consider adding a periodic Prow job that runs `scripts/upgrade-prow.sh` and
creates a PR (similar to how `generic-autobumper` works for prow.k8s.io).

### 5.2 ECR Pull-Through Cache (optional)

If `us-docker.pkg.dev` latency or rate limits become an issue, add a pull-through
cache rule for `us-docker.pkg.dev/k8s-infra-prow/images` in the existing ECR setup
(similar to the FluxCD pull-through cache already configured).

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Image pull failure from Artifact Registry | Low | High | ECR pull-through cache as fallback |
| CRD schema incompatibility | Very Low | Medium | Dry-run validation in script |
| Component startup failure | Low | High | Test on staging; Flux auto-rollback |
| Utility image mismatch | Low | High | All images use same tag (consistent) |

---

## Files Modified

| File | Change |
|------|--------|
| `prow/prow-version.yaml` | **NEW** — version tracking ConfigMap |
| `prow/config/values.yaml` | Update all image references |
| `prow/config/Chart.yaml` | Bump version to 0.5.0 |
| `flux/prow/crds/prowjob_customresourcedefinition.yaml` | Replace with latest upstream |
| `scripts/upgrade-prow.sh` | **NEW** — full upgrade script |
| `scripts/upgrade-prow-crd.sh` | **NEW** — CRD-only upgrade script |
| `bootstrap/flux.tf` | Add prow-version ConfigMap to substituteFrom (if templating) |
