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
