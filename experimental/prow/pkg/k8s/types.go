package k8s

import (
	prowv1 "sigs.k8s.io/prow/pkg/apis/prowjobs/v1"
)

// Prow type aliases for better organization
type (
	ProwJob           = prowv1.ProwJob
	ProwJobSpec       = prowv1.ProwJobSpec
	ProwJobStatus     = prowv1.ProwJobStatus
	ProwJobType       = prowv1.ProwJobType
	ProwJobAgent      = prowv1.ProwJobAgent
	ProwJobState      = prowv1.ProwJobState
	Refs              = prowv1.Refs
	Pull              = prowv1.Pull
	DecorationConfig  = prowv1.DecorationConfig
	GCSConfiguration  = prowv1.GCSConfiguration
	UtilityImages    = prowv1.UtilityImages
)

// ProwJob type constants
const (
	PresubmitJob  = prowv1.PresubmitJob
	PostsubmitJob = prowv1.PostsubmitJob
	PeriodicJob   = prowv1.PeriodicJob
	BatchJob      = prowv1.BatchJob
)

// ProwJob agent constants
const (
	KubernetesAgent = prowv1.KubernetesAgent
)

// ProwJob state constants
const (
	SchedulingState = prowv1.SchedulingState
	TriggeredState  = prowv1.TriggeredState
	PendingState    = prowv1.PendingState
	SuccessState    = prowv1.SuccessState
	FailureState    = prowv1.FailureState
	AbortedState    = prowv1.AbortedState
	ErrorState      = prowv1.ErrorState
)