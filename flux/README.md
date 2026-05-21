# Flux GitOps Configuration

This directory contains all Flux Kustomization resources that define what gets
deployed to the cluster and in what order.

## Structure

| File/Directory | Purpose |
|----------------|---------|
| `flux.yaml` | Flux self-management (Helm chart upgrades) |
| `ack.yaml` | ACK resource dependency chain (capability, cluster, addons, pod identities) |
| `prow.yaml` | Prow deployment (CRDs → image builds → charts) |
| `secrets.yaml` | Secrets Store CSI SecretProviderClass resources |
| `prometheus.yaml` | Prometheus + Grafana monitoring stack |
| `flux/` | Flux Helm release, source, and version config |
| `ack/` | ACK manifests (cluster, addons, pod identities, prow infra) |
| `prow/` | Prow Kubernetes resources (CRDs, build jobs, Helm values) |
| `secrets/` | SecretProviderClass and RBAC for Secrets Store CSI |
| `prometheus/` | Prometheus Helm release and config |
