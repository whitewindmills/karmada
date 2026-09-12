/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

type resourceUtilizationRatioTestCase struct {
	metrics           PodMetricsInfo
	requests          map[string]int64
	targetUtilization int32

	expectedUtilizationRatio   float64
	expectedCurrentUtilization int32
	expectedRawAverageValue    int64
	expectedErr                error
}

func (tc *resourceUtilizationRatioTestCase) runTest(t *testing.T) {
	actualUtilizationRatio, actualCurrentUtilization, actualRawAverageValue, actualErr := GetResourceUtilizationRatio(tc.metrics, tc.requests, tc.targetUtilization)

	if tc.expectedErr != nil {
		assert.Error(t, actualErr, "there should be an error getting the utilization ratio")
		assert.Contains(t, fmt.Sprintf("%v", actualErr), fmt.Sprintf("%v", tc.expectedErr), "the error message should be as expected")
		return
	}

	assert.NoError(t, actualErr, "there should be no error retrieving the utilization ratio")
	assert.Equal(t, tc.expectedUtilizationRatio, actualUtilizationRatio, "the utilization ratios should be as expected")
	assert.Equal(t, tc.expectedCurrentUtilization, actualCurrentUtilization, "the current utilization should be as expected")
	assert.Equal(t, tc.expectedRawAverageValue, actualRawAverageValue, "the raw average value should be as expected")
}

type metricUsageRatioTestCase struct {
	metrics     PodMetricsInfo
	targetUsage int64

	expectedUsageRatio   float64
	expectedCurrentUsage int64
}

func (tc *metricUsageRatioTestCase) runTest(t *testing.T) {
	actualUsageRatio, actualCurrentUsage := GetMetricUsageRatio(tc.metrics, tc.targetUsage)

	assert.Equal(t, tc.expectedUsageRatio, actualUsageRatio, "the usage ratios should be as expected")
	assert.Equal(t, tc.expectedCurrentUsage, actualCurrentUsage, "the current usage should be as expected")
}

func TestGetResourceUtilizationRatioBaseCase(t *testing.T) {
	tc := resourceUtilizationRatioTestCase{
		metrics: PodMetricsInfo{
			"test-pod-0": {Value: 50}, "test-pod-1": {Value: 76},
		},
		requests: map[string]int64{
			"test-pod-0": 100, "test-pod-1": 100,
		},
		targetUtilization:          50,
		expectedUtilizationRatio:   1.26,
		expectedCurrentUtilization: 63,
		expectedRawAverageValue:    63,
		expectedErr:                nil,
	}

	tc.runTest(t)
}

func TestGetResourceUtilizationRatioIgnorePodsWithNoRequest(t *testing.T) {
	tc := resourceUtilizationRatioTestCase{
		metrics: PodMetricsInfo{
			"test-pod-0": {Value: 50}, "test-pod-1": {Value: 76}, "test-pod-no-request": {Value: 100},
		},
		requests: map[string]int64{
			"test-pod-0": 100, "test-pod-1": 100,
		},
		targetUtilization:          50,
		expectedUtilizationRatio:   1.26,
		expectedCurrentUtilization: 63,
		expectedRawAverageValue:    63,
		expectedErr:                nil,
	}

	tc.runTest(t)
}

func TestGetResourceUtilizationRatioExtraRequest(t *testing.T) {
	tc := resourceUtilizationRatioTestCase{
		metrics: PodMetricsInfo{
			"test-pod-0": {Value: 50}, "test-pod-1": {Value: 76},
		},
		requests: map[string]int64{
			"test-pod-0": 100, "test-pod-1": 100, "test-pod-extra-request": 500,
		},
		targetUtilization:          50,
		expectedUtilizationRatio:   1.26,
		expectedCurrentUtilization: 63,
		expectedRawAverageValue:    63,
		expectedErr:                nil,
	}

	tc.runTest(t)
}

func TestGetResourceUtilizationRatioNoRequests(t *testing.T) {
	tc := resourceUtilizationRatioTestCase{
		metrics: PodMetricsInfo{
			"test-pod-0": {Value: 50}, "test-pod-1": {Value: 76},
		},
		requests:          map[string]int64{},
		targetUtilization: 50,

		expectedUtilizationRatio:   0,
		expectedCurrentUtilization: 0,
		expectedRawAverageValue:    0,
		expectedErr:                fmt.Errorf("no metrics returned matched known pods"),
	}

	tc.runTest(t)
}

func TestGetMetricUsageRatioBaseCase(t *testing.T) {
	tc := metricUsageRatioTestCase{
		metrics: PodMetricsInfo{
			"test-pod-0": {Value: 5000}, "test-pod-1": {Value: 10000},
		},
		targetUsage:          10000,
		expectedUsageRatio:   .75,
		expectedCurrentUsage: 7500,
	}

	tc.runTest(t)
}

func TestResourceUtilizationWithLargeMetrics(t *testing.T) {
	for name, tc := range map[string]resourceUtilizationRatioTestCase{
		"percentage multiplication": {
			metrics:                    PodMetricsInfo{"pod": {Value: 1 << 60}},
			requests:                   map[string]int64{"pod": 1 << 61},
			targetUtilization:          50,
			expectedUtilizationRatio:   1,
			expectedCurrentUtilization: 50,
			expectedRawAverageValue:    1 << 60,
		},
		"metric and request totals": {
			metrics:                    PodMetricsInfo{"first": {Value: math.MaxInt64}, "second": {Value: math.MaxInt64}},
			requests:                   map[string]int64{"first": math.MaxInt64, "second": math.MaxInt64},
			targetUtilization:          50,
			expectedUtilizationRatio:   2,
			expectedCurrentUtilization: 100,
			expectedRawAverageValue:    math.MaxInt64,
		},
		"request total alone": {
			metrics:                    PodMetricsInfo{"first": {Value: 1 << 60}, "second": {Value: 1 << 60}},
			requests:                   map[string]int64{"first": math.MaxInt64, "second": math.MaxInt64},
			targetUtilization:          50,
			expectedUtilizationRatio:   .24,
			expectedCurrentUtilization: 12,
			expectedRawAverageValue:    1 << 60,
		},
		"unrepresentable utilization": {
			metrics:           PodMetricsInfo{"pod": {Value: math.MaxInt64}},
			requests:          map[string]int64{"pod": 1},
			targetUtilization: 50,
			expectedErr:       fmt.Errorf("int32"),
		},
	} {
		t.Run(name, func(t *testing.T) { tc.runTest(t) })
	}
}

func TestMetricUsageAverageDoesNotOverflow(t *testing.T) {
	for _, value := range []int64{math.MaxInt64, math.MinInt64} {
		tc := metricUsageRatioTestCase{
			metrics:              PodMetricsInfo{"first": {Value: value}, "second": {Value: value}},
			targetUsage:          value,
			expectedUsageRatio:   1,
			expectedCurrentUsage: value,
		}
		tc.runTest(t)
	}
}

func TestAddMetricTotal(t *testing.T) {
	for _, tt := range []struct {
		total int64
		value int64
		fits  bool
		want  int64
	}{
		{total: math.MaxInt64 - 1, value: 1, fits: true, want: math.MaxInt64},
		{total: math.MaxInt64, value: 1},
		{total: math.MinInt64, value: -1},
		{total: math.MinInt64, value: 1, fits: true, want: math.MinInt64 + 1},
		{total: math.MinInt64, value: math.MaxInt64, fits: true, want: -1},
		{total: -10, value: 10, fits: true},
		{fits: true},
	} {
		sum, fits := addMetricTotal(tt.total, tt.value)
		assert.Equal(t, tt.fits, fits)
		if fits {
			assert.Equal(t, tt.want, sum)
		}
	}
}
