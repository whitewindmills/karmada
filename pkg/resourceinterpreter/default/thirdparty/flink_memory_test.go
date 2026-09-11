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

	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/util/helper"
)

func TestFlinkMemoryUnits(t *testing.T) {
	interpreter := NewConfigurableInterpreter()
	for _, tt := range []struct {
		memory string
		want   string
	}{
		{memory: "2048m", want: "2Gi"},
		{memory: "2048MB", want: "2Gi"},
		{memory: "1g", want: "1Gi"},
		{memory: "2G", want: "2Gi"},
		{memory: "1kb", want: "1Ki"},
		{memory: "2Tb", want: "2Ti"},
		{memory: " 2 mebibytes ", want: "2Mi"},
		{memory: "17bytes", want: "17"},
		{memory: "42", want: "42"},
		{memory: "2Gi", want: "2Gi"},
		{memory: "512MiB", want: "512Mi"},
		{memory: "1.5Gi", want: "1536Mi"},
		{memory: "0.5g", want: "500M"},
		{memory: "9007199254740993", want: "9007199254740993"},
	} {
		t.Run(tt.memory, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{
					"jobManager":  map[string]any{"resource": map[string]any{"memory": tt.memory}},
					"taskManager": map[string]any{"resource": map[string]any{"memory": "1m"}},
				},
			}}
			object.SetGroupVersionKind(schema.GroupVersionKind{Group: "flink.apache.org", Version: "v1beta1", Kind: "FlinkDeployment"})
			original := object.DeepCopy()
			want := resource.MustParse(tt.want)
			taskMemory := resource.MustParse("1Mi")
			components, enabled, err := interpreter.GetComponents(object)
			require.NoError(t, err)
			require.True(t, enabled)
			require.Len(t, components, 2)
			assert.True(t, components[0].ReplicaRequirements.ResourceRequest.Memory().Equal(want))
			assert.True(t, components[1].ReplicaRequirements.ResourceRequest.Memory().Equal(taskMemory))
			total := want.DeepCopy()
			total.Add(taskMemory)
			usage := helper.CalculateResourceUsage(&workv1alpha2.ResourceBinding{Spec: workv1alpha2.ResourceBindingSpec{
				Components: components, Clusters: []workv1alpha2.TargetCluster{{Name: "member"}},
			}})
			assert.True(t, usage.Memory().Equal(total), "component totals must use actual Flink memory bytes")

			replicas, requirements, enabled, err := interpreter.GetReplicas(object)
			require.NoError(t, err)
			require.True(t, enabled)
			assert.Equal(t, int32(2), replicas)
			if taskMemory.Cmp(want) > 0 {
				want = taskMemory
			}
			assert.True(t, requirements.ResourceRequest.Memory().Equal(want), "legacy interpretation must compare normalized memory quantities")
			assert.Equal(t, original, object, "memory normalization must not change the propagated specification")
		})
	}
}

func TestFlinkMemoryRejectsInvalidQuantities(t *testing.T) {
	interpreter := NewConfigurableInterpreter()
	for _, memory := range []string{"", "invalid", "1bogus"} {
		t.Run(memory, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{"jobManager": map[string]any{"resource": map[string]any{"memory": memory}}},
			}}
			object.SetGroupVersionKind(schema.GroupVersionKind{Group: "flink.apache.org", Version: "v1beta1", Kind: "FlinkDeployment"})
			_, _, err := interpreter.GetComponents(object)
			assert.Error(t, err)
			_, _, _, err = interpreter.GetReplicas(object)
			assert.Error(t, err)
		})
	}
}
