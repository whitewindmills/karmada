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

package thirdparty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSparkMemoryUsesWholeMiB(t *testing.T) {
	interpreter := NewConfigurableInterpreter()
	for _, tt := range []struct {
		name     string
		memory   string
		overhead string
		factor   string
		want     string
	}{
		{name: "bytes", memory: "1073741824b", want: "1408Mi"},
		{name: "kibibytes", memory: "1048576KB", want: "1408Mi"},
		{name: "unitless MiB", memory: "1024", want: "1408Mi"},
		{name: "case and whitespace", memory: " 1G ", want: "1408Mi"},
		{name: "newer Spark binary suffix alias", memory: "1GiB", want: "1408Mi"},
		{name: "heap truncates to MiB", memory: "1073741823b", want: "1407Mi"},
		{name: "explicit overhead truncates to MiB", memory: "1g", overhead: "402653183b", want: "1407Mi"},
		{name: "unitless overhead", memory: "1g", overhead: "384", want: "1408Mi"},
		{name: "fractional factor truncates overhead", memory: "10g", factor: "0.12", want: "11468Mi"},
		{name: "fractional heap is invalid", memory: "1.5g"},
		{name: "unknown unit is invalid", memory: "1bogus"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := map[string]any{"type": "Java"}
			for _, component := range []string{"driver", "executor"} {
				settings := map[string]any{"memory": tt.memory}
				if tt.overhead != "" {
					settings["memoryOverhead"] = tt.overhead
				}
				spec[component] = settings
			}
			if tt.factor != "" {
				spec["memoryOverheadFactor"] = tt.factor
			}
			object := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
			object.SetGroupVersionKind(schema.GroupVersionKind{Group: "sparkoperator.k8s.io", Version: "v1beta2", Kind: "SparkApplication"})
			components, enabled, err := interpreter.GetComponents(object)
			require.True(t, enabled)
			if tt.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, components, 2)
			for _, component := range components {
				got := component.ReplicaRequirements.ResourceRequest.Memory()
				assert.True(t, got.Equal(resource.MustParse(tt.want)), "%s memory = %s, want %s", component.Name, got.String(), tt.want)
			}
		})
	}
}
