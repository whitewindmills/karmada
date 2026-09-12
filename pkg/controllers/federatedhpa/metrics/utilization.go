/*
Copyright 2015 The Kubernetes Authors.

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
	"math/big"
)

// GetResourceUtilizationRatio takes in a set of metrics, a set of matching requests,
// and a target utilization percentage, and calculates the ratio of
// desired to actual utilization (returning that, the actual utilization, and the raw average value)
func GetResourceUtilizationRatio(metrics PodMetricsInfo, requests map[string]int64, targetUtilization int32) (utilizationRatio float64, currentUtilization int32, rawAverageValue int64, err error) {
	var metricsTotal, requestsTotal int64
	numEntries := 0
	for podName, metric := range metrics {
		request, exists := requests[podName]
		if !exists {
			continue
		}
		metricSum, metricFits := addMetricTotal(metricsTotal, metric.Value)
		requestSum, requestFits := addMetricTotal(requestsTotal, request)
		if !metricFits || !requestFits {
			return getResourceUtilizationRatioExact(metrics, requests, targetUtilization)
		}
		metricsTotal, requestsTotal = metricSum, requestSum
		numEntries++
	}
	if requestsTotal == 0 {
		return 0, 0, 0, fmt.Errorf("no metrics returned matched known pods")
	}
	if metricsTotal > math.MaxInt64/100 || metricsTotal < math.MinInt64/100 {
		return getResourceUtilizationRatioExact(metrics, requests, targetUtilization)
	}
	percentage := metricsTotal * 100 / requestsTotal
	if percentage < math.MinInt32 || percentage > math.MaxInt32 {
		return getResourceUtilizationRatioExact(metrics, requests, targetUtilization)
	}
	currentUtilization = int32(percentage) // #nosec G115 -- checked against int32 bounds above.
	return float64(currentUtilization) / float64(targetUtilization), currentUtilization, metricsTotal / int64(numEntries), nil
}

func addMetricTotal(total, value int64) (int64, bool) {
	sum := total + value
	return sum, (value >= 0 && sum >= total) || (value < 0 && sum < total)
}

func getResourceUtilizationRatioExact(metrics PodMetricsInfo, requests map[string]int64, targetUtilization int32) (float64, int32, int64, error) {
	// Federation totals can exceed int64 even when each metric and the resulting average fit.
	var metricsTotal, requestsTotal, value big.Int
	numEntries := 0

	for podName, metric := range metrics {
		request, hasRequest := requests[podName]
		if !hasRequest {
			// we check for missing requests elsewhere, so assuming missing requests == extraneous metrics
			continue
		}

		metricsTotal.Add(&metricsTotal, value.SetInt64(metric.Value))
		requestsTotal.Add(&requestsTotal, value.SetInt64(request))
		numEntries++
	}

	// if the set of requests is completely disjoint from the set of metrics,
	// then we could have an issue where the requests total is zero
	if requestsTotal.Sign() == 0 {
		return 0, 0, 0, fmt.Errorf("no metrics returned matched known pods")
	}

	var percentage big.Int
	percentage.Mul(&metricsTotal, value.SetInt64(100))
	percentage.Quo(&percentage, &requestsTotal)
	if !percentage.IsInt64() || percentage.Int64() < math.MinInt32 || percentage.Int64() > math.MaxInt32 {
		return 0, 0, 0, fmt.Errorf("resource utilization %s is outside the int32 range", percentage.String())
	}
	currentUtilization := int32(percentage.Int64()) // #nosec G115 -- checked against int32 bounds above.
	metricsTotal.Quo(&metricsTotal, value.SetInt64(int64(numEntries)))

	return float64(currentUtilization) / float64(targetUtilization), currentUtilization, metricsTotal.Int64(), nil
}

// GetMetricUsageRatio takes in a set of metrics and a target utilization value,
// and calculates the ratio of desired to actual utilization
// (returning that and the actual utilization)
func GetMetricUsageRatio(metrics PodMetricsInfo, targetUsage int64) (utilizationRatio float64, currentUsage int64) {
	var metricsTotal int64
	for _, metric := range metrics {
		sum, fits := addMetricTotal(metricsTotal, metric.Value)
		if !fits {
			return getMetricUsageRatioExact(metrics, targetUsage)
		}
		metricsTotal = sum
	}
	currentUsage = metricsTotal / int64(len(metrics))
	return float64(currentUsage) / float64(targetUsage), currentUsage
}

func getMetricUsageRatioExact(metrics PodMetricsInfo, targetUsage int64) (float64, int64) {
	var metricsTotal, value big.Int
	for _, metric := range metrics {
		metricsTotal.Add(&metricsTotal, value.SetInt64(metric.Value))
	}

	metricsTotal.Quo(&metricsTotal, value.SetInt64(int64(len(metrics))))
	currentUsage := metricsTotal.Int64()

	return float64(currentUsage) / float64(targetUsage), currentUsage
}
