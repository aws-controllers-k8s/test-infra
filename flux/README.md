# Flux GitOps Configuration

This directory contains all Flux Kustomization resources that define what gets
deployed to the cluster and in what order.

## Structure

| File/Directory | Purpose |
|----------------|---------|
| `flux.yaml` | Flux self-management (Helm chart upgrades) |
| `ack.yaml` | ACK resource dependency chain (capability, cluster, addons, pod identities) |
| `ack-build-cluster.yaml` | ACK resources for the dedicated Prow build cluster |
| `prow.yaml` | Prow deployment (CRDs → image builds → charts → build cluster wiring) |
| `secrets.yaml` | Secrets Store CSI SecretProviderClass resources |
| `flux/` | Flux Helm release, source, and version config |
| `ack/` | ACK manifests (control-plane cluster, addons, pod identities, prow infra) |
| `ack/build-cluster/` | Dedicated Prow build cluster (VPC, subnets, NAT, roles, cluster, access entries, pod identities) |
| `prow/` | Prow Kubernetes resources (CRDs, build jobs, Helm values, build cluster connection/kubeconfig/resources) |
| `secrets/` | SecretProviderClass and RBAC for Secrets Store CSI |
