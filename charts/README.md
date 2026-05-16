# Vendored Helm Charts

Extracted Helm charts committed to the repo. Both Terraform bootstrap and
Flux self-updates reference these directly — no external Helm repo dependency
at any point in the lifecycle.

## Structure

```
charts/
└── flux2-2.8.6/       # Extracted flux2 chart (contains Chart.yaml, templates/, etc.)
    ├── Chart.yaml
    ├── values.yaml
    └── templates/
```

## Updating

```bash
# Edit flux/flux-version.yaml, then:
./scripts/pull-flux-chart.sh

# Also update the chart path in flux/flux/helm-release.yaml:
#   spec.chart.spec.chart: ./charts/flux2-<new-version>

git add charts/ flux/
git commit -m "chore(flux): vendor flux2 chart <version>"
```
