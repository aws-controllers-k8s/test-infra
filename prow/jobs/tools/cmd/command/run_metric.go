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

package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// runSucceededMetric is what the CloudWatch alarms watch: 1 when the run did its
// job, 0 when it did not. A run that never finishes publishes nothing, which the
// missing-data alarm catches.
const runSucceededMetric = "RunSucceeded"

// runMetricTimeout bounds the publish. It runs on a fresh context, since the run's
// may already be cancelled, and must fit in the ProwJob's grace period.
const runMetricTimeout = 30 * time.Second

// errCapReached is returned when the open-issue cap was the only reason a run
// failed. The job still goes red, but the run worked, so the metric reports 1:
// the cap binds by design during a rollout and is not something to page on.
var errCapReached = errors.New("open-issue cap reached")

// capReachedError keeps runError's message as is while matching errCapReached.
type capReachedError string

func (e capReachedError) Error() string      { return string(e) }
func (capReachedError) Is(target error) bool { return target == errCapReached }

// runSucceededValue maps a run's error to the metric value.
func runSucceededValue(runErr error) float64 {
	if runErr == nil || errors.Is(runErr, errCapReached) {
		return 1
	}
	return 0
}

// metricPutter is the part of the CloudWatch client the publisher uses.
type metricPutter interface {
	PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error)
}

func newMetricPutter(ctx context.Context, region string) (metricPutter, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}
	return cloudwatch.NewFromConfig(cfg), nil
}

// publishRunSucceeded writes one RunSucceeded datapoint to namespace.
func publishRunSucceeded(ctx context.Context, client metricPutter, namespace string, value float64) error {
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(namespace),
		MetricData: []cwtypes.MetricDatum{{
			MetricName: aws.String(runSucceededMetric),
			Value:      aws.Float64(value),
			Unit:       cwtypes.StandardUnitCount,
			Timestamp:  aws.Time(time.Now()),
		}},
	})
	return err
}
