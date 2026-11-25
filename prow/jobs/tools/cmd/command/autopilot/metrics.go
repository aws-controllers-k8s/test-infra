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

package autopilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

// The Dispatcher's CloudWatch metrics (Plan 10), one PutMetricData call per
// tick in --metrics-namespace (ACK/Autopilot in the periodic). The names and
// dimensions are a contract with the alarms: do not rename them or add
// dimensions.
const (
	// MetricRunSucceeded is 1 when the tick completed and 0 when it returned
	// an error, with dimension Job=autopilot-dispatch. A tick that never ends
	// publishes nothing, which the missing-data alarm catches.
	MetricRunSucceeded = "RunSucceeded"
	// MetricWorkItemsReapedToday is a gauge: dispatcher.ReapedToday.
	MetricWorkItemsReapedToday = "WorkItemsReapedToday"
	// The rest count what this tick did (dispatcher.Counts); 0 is published.
	MetricGateFirstPass  = "GateFirstPass"
	MetricGateFirstFail  = "GateFirstFail"
	MetricRunsCompleted  = "RunsCompleted"
	MetricRunsFailed     = "RunsFailed"
	MetricItemsEscalated = "ItemsEscalated"

	runSucceededJobDimension = "Job"
	dispatchJobName          = "autopilot-dispatch"
)

// metricTimeout bounds the publish. It runs on a fresh context, since the
// tick's may be cancelled, and must fit in the job's grace period.
const metricTimeout = 30 * time.Second

type metricsOptions struct {
	Namespace, Region string
}

func (m metricsOptions) validate() error {
	if m.Namespace != "" && m.Region == "" {
		return errors.New("--metrics-region is required with --metrics-namespace")
	}
	return nil
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

type metricValue struct {
	name  string
	value float64
}

// tickMetrics are the values a tick publishes, in a fixed order. A tick that
// returned an error publishes RunSucceeded=0 alone: whatever it counted was
// not saved, and the next tick does it again.
func tickMetrics(res *dispatcher.Result, runErr error) []metricValue {
	if runErr != nil || res == nil {
		return []metricValue{{MetricRunSucceeded, 0}}
	}
	c := res.Counts
	return []metricValue{
		{MetricRunSucceeded, 1},
		{MetricWorkItemsReapedToday, float64(res.Reaped)},
		{MetricGateFirstPass, float64(c.GateFirstPass)},
		{MetricGateFirstFail, float64(c.GateFirstFail)},
		{MetricRunsCompleted, float64(c.RunsCompleted)},
		{MetricRunsFailed, float64(c.RunsFailed)},
		{MetricItemsEscalated, float64(c.ItemsEscalated)},
	}
}

func metricData(values []metricValue, now time.Time) []cwtypes.MetricDatum {
	out := make([]cwtypes.MetricDatum, 0, len(values))
	for _, v := range values {
		d := cwtypes.MetricDatum{
			MetricName: aws.String(v.name),
			Value:      aws.Float64(v.value),
			Unit:       cwtypes.StandardUnitCount,
			Timestamp:  aws.Time(now),
		}
		if v.name == MetricRunSucceeded {
			d.Dimensions = []cwtypes.Dimension{{Name: aws.String(runSucceededJobDimension), Value: aws.String(dispatchJobName)}}
		}
		out = append(out, d)
	}
	return out
}

// publishTickMetrics prints the tick's metric values and, when m names a
// namespace, publishes them in one PutMetricData call. A dry run publishes
// too: until ledger writes go live the periodic is a dry run, and its
// RunSucceeded is what the alarms watch. Publishing is best effort, as in
// detect-api-changes: a failure is printed and never changes the tick's exit
// status, so a CloudWatch outage cannot turn a good tick red (and a bad tick
// stays red). The missing-data alarm catches a publisher that keeps failing.
func publishTickMetrics(m metricsOptions, res *dispatcher.Result, runErr error, now time.Time, out io.Writer,
	newPutter func(context.Context, string) (metricPutter, error)) {
	values := tickMetrics(res, runErr)
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprintf("%s=%v", v.name, v.value))
	}
	line := "metrics " + strings.Join(parts, " ")
	if m.Namespace == "" {
		fmt.Fprintln(out, line+" (not published: --metrics-namespace is empty)")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricTimeout)
	defer cancel()
	putter, err := newPutter(ctx, m.Region)
	if err == nil {
		_, err = putter.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace:  aws.String(m.Namespace),
			MetricData: metricData(values, now),
		})
	}
	if err != nil {
		fmt.Fprintf(out, "%s (WARNING unable to publish to %s: %v)\n", line, m.Namespace, err)
		return
	}
	fmt.Fprintf(out, "%s (published to %s)\n", line, m.Namespace)
}
