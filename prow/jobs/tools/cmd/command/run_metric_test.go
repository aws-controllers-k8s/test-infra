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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSucceededValue(t *testing.T) {
	assert.Equal(t, 1.0, runSucceededValue(nil))
	assert.Equal(t, 0.0, runSucceededValue(errors.New("boom")))

	// The cap alone still reports success; with any other reason it does not.
	capOnly := runError(nil, 1, []string{"svc1"}, nil, nil, false)
	require.Error(t, capOnly)
	assert.ErrorIs(t, capOnly, errCapReached)
	assert.ErrorContains(t, capOnly, "open-issue cap of 1 reached; no issue filed for: [svc1]")
	assert.Equal(t, 1.0, runSucceededValue(capOnly))

	withAnalysis := runError(nil, 1, []string{"svc1"}, []string{"svc2"}, nil, false)
	assert.NotErrorIs(t, withAnalysis, errCapReached)
	assert.Equal(t, 0.0, runSucceededValue(withAnalysis))

	withAbort := runError(errors.New("abort"), 1, []string{"svc1"}, nil, nil, false)
	assert.Equal(t, 0.0, runSucceededValue(withAbort))
}

type fakeMetricPutter struct {
	in  *cloudwatch.PutMetricDataInput
	err error
}

func (f *fakeMetricPutter) PutMetricData(
	_ context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options),
) (*cloudwatch.PutMetricDataOutput, error) {
	f.in = in
	return &cloudwatch.PutMetricDataOutput{}, f.err
}

func TestPublishRunSucceeded(t *testing.T) {
	fake := &fakeMetricPutter{}
	require.NoError(t, publishRunSucceeded(context.Background(), fake, "ACK/APIChangeNotification", 0))
	assert.Equal(t, "ACK/APIChangeNotification", aws.ToString(fake.in.Namespace))
	require.Len(t, fake.in.MetricData, 1)
	assert.Equal(t, runSucceededMetric, aws.ToString(fake.in.MetricData[0].MetricName))
	assert.Equal(t, 0.0, aws.ToFloat64(fake.in.MetricData[0].Value))

	fake.err = errors.New("AccessDenied")
	assert.ErrorContains(t, publishRunSucceeded(context.Background(), fake, "ns", 1), "AccessDenied")
}
