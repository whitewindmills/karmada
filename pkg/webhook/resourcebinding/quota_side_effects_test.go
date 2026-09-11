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

package resourcebinding

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/features"
)

func TestInvalidComponentsDoNotReserveQuota(t *testing.T) {
	originalGates := features.FeatureGate.DeepCopy()
	t.Cleanup(func() { features.FeatureGate = originalGates })
	require.NoError(t, features.FeatureGate.Set(fmt.Sprintf("%s=true,%s=true",
		features.FederatedQuotaEnforcement, features.MultiplePodTemplatesScheduling)))

	for _, name := range []string{"", "first"} {
		t.Run("second component name="+name, func(t *testing.T) {
			first := workv1alpha2.Component{Name: "first", Replicas: 1,
				ReplicaRequirements: &workv1alpha2.ComponentReplicaRequirements{
					ResourceRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				}}
			second := *first.DeepCopy()
			second.Name = name
			binding := makeTestRB("default", "binding",
				WithClusters([]workv1alpha2.TargetCluster{{Name: "member"}}),
				WithComponents([]workv1alpha2.Component{first, second}))
			quota := makeTestFRQ("default", "quota",
				WithOverallLimits(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}),
				WithOverallUsed(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}))
			statusWrites := 0
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(quota).WithStatusSubresource(quota).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						statusWrites++
						return c.SubResource(subresource).Update(ctx, obj, opts...)
					},
				}).Build()
			validator := &ValidatingAdmission{Client: c, Decoder: &fakeDecoder{decodeObj: binding}}
			response := validator.Handle(t.Context(),
				newAdmissionRequestBuilder(t, admissionv1.Create, binding.Namespace, binding.Name, "invalid-components").
					WithObject(binding).Build())
			assert.False(t, response.Allowed)
			require.NotNil(t, response.Result)
			assert.Contains(t, response.Result.Message, "spec.components[1].name")
			assert.Zero(t, statusWrites, "invalid bindings must not reserve quota before denial")
			actual := &policyv1alpha1.FederatedResourceQuota{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(quota), actual))
			assert.Equal(t, quota.Status, actual.Status)
		})
	}
}
