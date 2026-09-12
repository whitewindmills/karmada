/*
Copyright 2026 The Karmada Authors.

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
	"testing"
)

func BenchmarkResourceUtilization(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprintf("pods=%d", count), func(b *testing.B) {
			metrics := make(PodMetricsInfo, count)
			requests := make(map[string]int64, count)
			for i := range count {
				name := fmt.Sprintf("pod-%d", i)
				metrics[name] = PodMetric{Value: 500}
				requests[name] = 1000
			}
			b.ReportAllocs()
			for b.Loop() {
				ratio, utilization, average, err := GetResourceUtilizationRatio(metrics, requests, 50)
				if err != nil || ratio != 1 || utilization != 50 || average != 500 {
					b.Fatal("incorrect metric calculation", ratio, utilization, average, err)
				}
			}
		})
	}
}
