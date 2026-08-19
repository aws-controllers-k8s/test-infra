{{/*
Expand the name of the chart.
*/}}
{{- define "prow-config.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "prow-config.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "prow-config.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Scheduling for Prow control-plane components.
Pins pods to the dedicated, tainted `prow-control-plane` NodePool, isolating
them from the general e2e compute pool.
*/}}
{{- define "prow-config.controlPlaneScheduling" -}}
nodeSelector:
  ack.aws.dev/node-role: control-plane
tolerations:
- key: ack.aws.dev/control-plane
  operator: Equal
  value: "true"
  effect: NoSchedule
{{- end }}

{{/*
Build-cluster kubeconfig arg for the components that reach the build cluster.
Emits the --kubeconfig flag only when the build cluster is enabled; Prow then
loads every context in the file as an additional build cluster (the in-cluster
`default` context is always available regardless).
*/}}
{{- define "prow-config.buildClusterArg" -}}
{{- if .Values.buildCluster.enabled }}
- --kubeconfig={{ .Values.buildCluster.kubeconfigMountPath }}/config
{{- end }}
{{- end }}

{{/*
Volume mount for the build-cluster kubeconfig ConfigMap. Gated on enablement.
*/}}
{{- define "prow-config.buildClusterVolumeMount" -}}
{{- if .Values.buildCluster.enabled }}
- name: build-cluster-kubeconfig
  mountPath: {{ .Values.buildCluster.kubeconfigMountPath }}
  readOnly: true
{{- end }}
{{- end }}

{{/*
Volume backing the build-cluster kubeconfig. It is a ConfigMap (no secret
material — the `build` context authenticates via an in-container exec plugin
running aws-iam-authenticator, so only cluster coordinates are stored).
*/}}
{{- define "prow-config.buildClusterVolume" -}}
{{- if .Values.buildCluster.enabled }}
- name: build-cluster-kubeconfig
  configMap:
    name: build-cluster-kubeconfig
{{- end }}
{{- end }}

{{/*
Resolve a Prow component image: the explicit override if set, otherwise composed from
imageMirror.

Called as:
  include "prow-config.image" (dict "ctx" . "name" "<ecr-repo>" "override" .Values.x.image)

THE OVERRIDE IS RESOLVED INSIDE THIS HELPER, not with `default` at the call site. Go
templates evaluate arguments eagerly, so `.Values.x.image | default (include ...)` runs the
composition even when the override is set - and its `required` calls then fail on the
imageMirror values that a Flux-driven render has no reason to supply. Verified by hitting
exactly that. Keeping the branch here means the composition is only reached when it is
actually needed.

The suffix is the ECR repository name, which is NOT always the values key - the
statusreconciler component lives at prow/status-reconciler. Each call site passes the
repository name explicitly rather than deriving it, so the mapping stays visible where it
matters.

Only reached when a component's `image` value is empty; an explicit image always wins. See
the imageMirror block in values.yaml for why this composition belongs to the chart rather
than to whatever is supplying values.

`required` on all four inputs so a missing one fails at render time. Without it a blank
accountId would silently produce ".dkr.ecr..amazonaws.com/prow/deck:-ack." and the failure
would surface as an ImagePullBackOff long after the sync reported healthy.
*/}}
{{- define "prow-config.image" -}}
{{- if .override -}}
{{- .override -}}
{{- else -}}
{{- $m := .ctx.Values.imageMirror -}}
{{- $account := required "imageMirror.accountId is required to compose a Prow image reference" $m.accountId -}}
{{- $region := required "imageMirror.region is required to compose a Prow image reference" $m.region -}}
{{- $version := required "imageMirror.prowVersion is required to compose a Prow image reference" $m.prowVersion -}}
{{- $revision := required "imageMirror.prowPatchRevision is required to compose a Prow image reference" $m.prowPatchRevision -}}
{{/*
  toString on every input, because an AWS account id is a 12-digit string that YAML will
  happily read as a number if it is ever written unquoted in a values file. printf %s on a
  float64 yields "%!s(float64=8.6987147623e+10)", which is a syntactically valid image
  reference that fails at pull time rather than at render time. Verified by hitting exactly
  that. Argo CD's parameters and Helm's --set both keep it a string; a hand-written values
  file is the case this guards.
*/}}
{{- printf "%s.dkr.ecr.%s.amazonaws.com/prow/%s:%s-ack.%s" (toString $account) (toString $region) .name (toString $version) (toString $revision) -}}
{{- end -}}
{{- end }}
