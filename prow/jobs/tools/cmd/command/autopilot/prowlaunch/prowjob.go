// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package prowlaunch

// The ProwJob fields the Dispatcher writes and reads, with the JSON names of
// sigs.k8s.io/prow pkg/apis/prowjobs/v1 and k8s.io/api core/v1. Decoding
// ignores every other field. Importing those modules would add sigs.k8s.io/prow
// and client-go to a binary every Dispatcher tick builds from source.

type ProwJob struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       Spec       `json:"spec"`
	Status     JobStatus  `json:"status"`
}

type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
}

type Spec struct {
	Type             string            `json:"type,omitempty"`
	Agent            string            `json:"agent,omitempty"`
	Cluster          string            `json:"cluster,omitempty"`
	Job              string            `json:"job,omitempty"`
	ExtraRefs        []Refs            `json:"extra_refs,omitempty"`
	DecorationConfig *DecorationConfig `json:"decoration_config,omitempty"`
	PodSpec          *PodSpec          `json:"pod_spec,omitempty"`
}

type Refs struct {
	Org       string `json:"org"`
	Repo      string `json:"repo"`
	BaseRef   string `json:"base_ref,omitempty"`
	PathAlias string `json:"path_alias,omitempty"`
	WorkDir   bool   `json:"workdir,omitempty"`
}

type DecorationConfig struct {
	// Timeout and GracePeriod are Go durations ("5h0m0s"), as prow's
	// Duration marshals them.
	Timeout          string            `json:"timeout,omitempty"`
	GracePeriod      string            `json:"grace_period,omitempty"`
	UtilityImages    *UtilityImages    `json:"utility_images,omitempty"`
	GCSConfiguration *GCSConfiguration `json:"gcs_configuration,omitempty"`
	CensorSecrets    *bool             `json:"censor_secrets,omitempty"`
	CensoringOptions *CensoringOptions `json:"censoring_options,omitempty"`
}

type UtilityImages struct {
	CloneRefs  string `json:"clonerefs,omitempty"`
	InitUpload string `json:"initupload,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
	Sidecar    string `json:"sidecar,omitempty"`
}

type GCSConfiguration struct {
	Bucket       string `json:"bucket,omitempty"`
	PathStrategy string `json:"path_strategy,omitempty"`
}

type CensoringOptions struct {
	IncludeDirectories []string `json:"include_directories,omitempty"`
}

type JobStatus struct {
	StartTime   string `json:"startTime,omitempty"`
	State       string `json:"state,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	BuildID     string `json:"build_id,omitempty"`
}

type PodSpec struct {
	Containers         []Container `json:"containers"`
	RestartPolicy      string      `json:"restartPolicy,omitempty"`
	ServiceAccountName string      `json:"serviceAccountName,omitempty"`
	// AutomountServiceAccountToken false keeps the ServiceAccount token out
	// of the pod (the dod job; Plan 9 review F14).
	AutomountServiceAccountToken *bool    `json:"automountServiceAccountToken,omitempty"`
	Volumes                      []Volume `json:"volumes,omitempty"`
}

type Container struct {
	Name            string               `json:"name"`
	Image           string               `json:"image,omitempty"`
	Command         []string             `json:"command,omitempty"`
	Args            []string             `json:"args,omitempty"`
	Env             []EnvVar             `json:"env,omitempty"`
	Resources       ResourceRequirements `json:"resources"`
	VolumeMounts    []VolumeMount        `json:"volumeMounts,omitempty"`
	SecurityContext *SecurityContext     `json:"securityContext,omitempty"`
}

type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	SecretKeyRef *SecretKeySelector `json:"secretKeyRef,omitempty"`
}

type SecretKeySelector struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
}

type ResourceRequirements struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
	MountPath string `json:"mountPath"`
}

type SecurityContext struct {
	Privileged *bool `json:"privileged,omitempty"`
}

type Volume struct {
	Name     string              `json:"name"`
	EmptyDir *EmptyDirSource     `json:"emptyDir,omitempty"`
	HostPath *HostPathSource     `json:"hostPath,omitempty"`
	CSI      *CSISource          `json:"csi,omitempty"`
	Secret   *SecretVolumeSource `json:"secret,omitempty"`
}

type SecretVolumeSource struct {
	SecretName string `json:"secretName"`
}

type EmptyDirSource struct{}

type HostPathSource struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
}

type CSISource struct {
	Driver           string            `json:"driver"`
	ReadOnly         *bool             `json:"readOnly,omitempty"`
	VolumeAttributes map[string]string `json:"volumeAttributes,omitempty"`
}
