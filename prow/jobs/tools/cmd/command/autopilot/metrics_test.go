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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

type fakeMetricPutter struct {
	calls []*cloudwatch.PutMetricDataInput
	err   error
}

func (f *fakeMetricPutter) PutMetricData(
	_ context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options),
) (*cloudwatch.PutMetricDataOutput, error) {
	f.calls = append(f.calls, in)
	return &cloudwatch.PutMetricDataOutput{}, f.err
}

// putterFor hands out fake; regions records each region asked for.
func putterFor(fake *fakeMetricPutter, regions *[]string) func(context.Context, string) (metricPutter, error) {
	return func(_ context.Context, region string) (metricPutter, error) {
		*regions = append(*regions, region)
		return fake, nil
	}
}

type datum struct {
	Name  string
	Value float64
	Dims  map[string]string
}

func data(in *cloudwatch.PutMetricDataInput) []datum {
	var out []datum
	for _, d := range in.MetricData {
		dims := map[string]string{}
		for _, dm := range d.Dimensions {
			dims[aws.ToString(dm.Name)] = aws.ToString(dm.Value)
		}
		out = append(out, datum{aws.ToString(d.MetricName), aws.ToFloat64(d.Value), dims})
	}
	return out
}

// TestTickMetricsContract pins the names and dimensions the alarms use.
func TestTickMetricsContract(t *testing.T) {
	res := &dispatcher.Result{Reaped: 3, Counts: dispatcher.Counts{
		GateFirstPass: 1, GateFirstFail: 2, RunsCompleted: 4, RunsFailed: 5, ItemsEscalated: 6}}
	fake := &fakeMetricPutter{}
	var regions []string
	var out strings.Builder
	publishTickMetrics(metricsOptions{Namespace: "ACK/Autopilot", Region: "us-west-2"}, res, nil, tickTime, &out, putterFor(fake, &regions))
	require.Len(t, fake.calls, 1, "one PutMetricData call per tick")
	assert.Equal(t, []string{"us-west-2"}, regions)
	in := fake.calls[0]
	assert.Equal(t, "ACK/Autopilot", aws.ToString(in.Namespace))
	none := map[string]string{}
	assert.Equal(t, []datum{
		{"RunSucceeded", 1, map[string]string{"Job": "autopilot-dispatch"}},
		{"WorkItemsReapedToday", 3, none},
		{"GateFirstPass", 1, none},
		{"GateFirstFail", 2, none},
		{"RunsCompleted", 4, none},
		{"RunsFailed", 5, none},
		{"ItemsEscalated", 6, none},
	}, data(in))
	for _, d := range in.MetricData {
		assert.Equal(t, cwtypes.StandardUnitCount, d.Unit)
		assert.Equal(t, tickTime, aws.ToTime(d.Timestamp))
	}
	assert.Equal(t, "metrics RunSucceeded=1 WorkItemsReapedToday=3 GateFirstPass=1 GateFirstFail=2 RunsCompleted=4 RunsFailed=5 ItemsEscalated=6 (published to ACK/Autopilot)\n", out.String())
}

func TestTickMetricsZerosArePublished(t *testing.T) {
	fake := &fakeMetricPutter{}
	var regions []string
	publishTickMetrics(metricsOptions{Namespace: "ns", Region: "r"}, &dispatcher.Result{}, nil, tickTime, &strings.Builder{}, putterFor(fake, &regions))
	require.Len(t, fake.calls, 1)
	got := data(fake.calls[0])
	require.Len(t, got, 7)
	assert.Equal(t, 1.0, got[0].Value)
	for _, d := range got[1:] {
		assert.Equal(t, 0.0, d.Value, d.Name)
	}
}

// TestTickMetricsOnError: a failed tick publishes RunSucceeded=0 alone, and
// a failed publish is only printed.
func TestTickMetricsOnError(t *testing.T) {
	fake := &fakeMetricPutter{err: errors.New("AccessDenied")}
	var regions []string
	var out strings.Builder
	res := &dispatcher.Result{Counts: dispatcher.Counts{RunsFailed: 1}}
	publishTickMetrics(metricsOptions{Namespace: "ACK/Autopilot", Region: "us-west-2"}, res, errors.New("save failed"), tickTime, &out, putterFor(fake, &regions))
	require.Len(t, fake.calls, 1)
	assert.Equal(t, []datum{{"RunSucceeded", 0, map[string]string{"Job": "autopilot-dispatch"}}}, data(fake.calls[0]))
	assert.Equal(t, "metrics RunSucceeded=0 (WARNING unable to publish to ACK/Autopilot: AccessDenied)\n", out.String())

	out.Reset()
	publishTickMetrics(metricsOptions{Namespace: "ns", Region: "r"}, nil, errors.New("github"), tickTime, &out,
		func(context.Context, string) (metricPutter, error) { return nil, errors.New("no credentials") })
	assert.Equal(t, "metrics RunSucceeded=0 (WARNING unable to publish to ns: no credentials)\n", out.String())
}

func TestTickMetricsOffByDefault(t *testing.T) {
	var out strings.Builder
	publishTickMetrics(metricsOptions{}, &dispatcher.Result{}, nil, tickTime, &out,
		func(context.Context, string) (metricPutter, error) {
			t.Fatal("no namespace: no AWS client")
			return nil, nil
		})
	assert.Contains(t, out.String(), "(not published: --metrics-namespace is empty)")
}

func TestMetricsOptionsValidate(t *testing.T) {
	assert.NoError(t, metricsOptions{}.validate())
	assert.NoError(t, metricsOptions{Namespace: "ACK/Autopilot", Region: "us-west-2"}.validate())
	assert.ErrorContains(t, metricsOptions{Namespace: "ACK/Autopilot"}.validate(), "--metrics-region is required")
}

func TestDispatchMetricsFlagsAreRegistered(t *testing.T) {
	for _, name := range []string{"metrics-namespace", "metrics-region"} {
		f := dispatchCmd.Flags().Lookup(name)
		require.NotNil(t, f, name)
		assert.Equal(t, "", f.DefValue, "publishing is off by default")
	}
}

// TestDispatchDryRunComputesMetrics: a dry-run tick returns its result, so
// its counts are printed; with the flags empty nothing is published.
func TestDispatchDryRunComputesMetrics(t *testing.T) {
	dir := t.TempDir()
	copyPolicy(t, dir)
	o := opts(dir)
	o.DryRun = true
	var out strings.Builder
	res, err := runDispatch(context.Background(), fakeGitHub(t), o, tickTime, &out)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, dispatcher.Counts{}, res.Counts)
	out.Reset()
	publishTickMetrics(metricsOptions{}, res, err, tickTime, &out, func(context.Context, string) (metricPutter, error) {
		t.Fatal("flags empty: nothing is published")
		return nil, nil
	})
	assert.Equal(t, "metrics RunSucceeded=1 WorkItemsReapedToday=0 GateFirstPass=0 GateFirstFail=0 RunsCompleted=0 RunsFailed=0 ItemsEscalated=0 (not published: --metrics-namespace is empty)\n", out.String())

	// A tick that fails before Tick runs has no result.
	o.Owner = ""
	res, err = runDispatch(context.Background(), fakeGitHub(t), o, tickTime, &out)
	assert.Error(t, err)
	assert.Nil(t, res)
}
