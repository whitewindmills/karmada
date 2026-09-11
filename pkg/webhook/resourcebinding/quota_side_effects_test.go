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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestNegativeUsageInputsDoNotReleaseQuota(t *testing.T) {
	originalGates := features.FeatureGate.DeepCopy()
	t.Cleanup(func() { features.FeatureGate = originalGates })
	require.NoError(t, features.FeatureGate.Set(fmt.Sprintf("%s=true,%s=true",
		features.FederatedQuotaEnforcement, features.MultiplePodTemplatesScheduling)))

	for _, tt := range []struct {
		name      string
		component bool
		replicas  int32
		cpu       string
		field     string
		allowed   bool
	}{
		{name: "scheduled replicas", replicas: -1, cpu: "100m", field: "spec.clusters[0].replicas"},
		{name: "replica quantity", replicas: 1, cpu: "-100m", field: "spec.replicaRequirements.resourceRequest[cpu]"},
		{name: "component replicas", component: true, replicas: -1, cpu: "100m", field: "spec.components[0].replicas"},
		{name: "component quantity", component: true, replicas: 1, cpu: "-100m", field: "spec.components[0].replicaRequirements.resourceRequest[cpu]"},
		{name: "zero replicas", replicas: 0, cpu: "100m", allowed: true},
		{name: "zero quantity", replicas: 1, cpu: "0", allowed: true},
		{name: "zero component replicas", component: true, replicas: 0, cpu: "100m", allowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tt.cpu)}
			binding := makeTestRB("default", "binding",
				WithClusters([]workv1alpha2.TargetCluster{{Name: "member", Replicas: tt.replicas}}),
				WithReplicaRequirements(requests))
			if tt.component {
				binding.Spec.ReplicaRequirements = nil
				binding.Spec.Clusters[0].Replicas = 0
				binding.Spec.Components = []workv1alpha2.Component{{
					Name: "component", Replicas: tt.replicas,
					ReplicaRequirements: &workv1alpha2.ComponentReplicaRequirements{ResourceRequest: requests},
				}}
			}
			quota := makeTestFRQ("default", "quota",
				WithOverallLimits(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}),
				WithOverallUsed(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")}))
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(quota).WithStatusSubresource(quota).Build()
			validator := &ValidatingAdmission{Client: c, Decoder: &fakeDecoder{decodeObj: binding}}
			response := validator.Handle(t.Context(),
				newAdmissionRequestBuilder(t, admissionv1.Create, binding.Namespace, binding.Name, "negative-usage").
					WithObject(binding).Build())
			assert.Equal(t, tt.allowed, response.Allowed, "negative requested usage must not be treated as a quota release")
			require.NotNil(t, response.Result)
			if tt.field != "" {
				assert.Contains(t, response.Result.Message, tt.field)
			}
			actual := &policyv1alpha1.FederatedResourceQuota{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(quota), actual))
			assert.Equal(t, quota.Status, actual.Status, "invalid requests must leave reservations unchanged")
		})
	}
}

func TestNonnegativeQuotaInputsPermitScaleDown(t *testing.T) {
	originalGates := features.FeatureGate.DeepCopy()
	t.Cleanup(func() { features.FeatureGate = originalGates })
	require.NoError(t, features.FeatureGate.Set(fmt.Sprintf("%s=true", features.FederatedQuotaEnforcement)))
	oldBinding := makeTestRB("default", "binding",
		WithClusters([]workv1alpha2.TargetCluster{{Name: "member", Replicas: 2}}),
		WithReplicaRequirements(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}))
	binding := oldBinding.DeepCopy()
	binding.Spec.Clusters[0].Replicas = 1
	quota := makeTestFRQ("default", "quota",
		WithOverallLimits(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}),
		WithOverallUsed(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")}))
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(quota).WithStatusSubresource(quota).Build()
	validator := &ValidatingAdmission{Client: c, Decoder: &fakeDecoder{decodeObj: binding, rawDecodedObj: oldBinding}}
	response := validator.Handle(t.Context(),
		newAdmissionRequestBuilder(t, admissionv1.Update, binding.Namespace, binding.Name, "scale-down").
			WithObject(binding).WithOldObject(oldBinding).Build())
	require.True(t, response.Allowed, "%+v", response.Result)
	actual := &policyv1alpha1.FederatedResourceQuota{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(quota), actual))
	assert.True(t, actual.Status.OverallUsed.Cpu().Equal(resource.MustParse("100m")), "valid scale-down must release its actual usage")
}

func TestQuotaConflictRetryDoesNotReapplyCommittedDeltas(t *testing.T) {
	for _, tt := range []struct {
		name       string
		initial    string
		delta      string
		expected   string
		limit      string
		dryRun     bool
		recreate   bool
		firstCalls int
	}{
		{name: "reserve once", initial: "100m", delta: "100m", expected: "200m", limit: "200m", firstCalls: 1},
		{name: "release once", initial: "300m", delta: "-100m", expected: "200m", limit: "1", firstCalls: 1},
		{name: "dry run persists nothing", initial: "100m", delta: "100m", expected: "100m", limit: "200m", dryRun: true, firstCalls: 2},
		{name: "recreated quota requires its own reservation", initial: "100m", delta: "100m", expected: "200m", limit: "200m", recreate: true, firstCalls: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first := makeTestFRQ("default", "first",
				WithOverallLimits(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tt.limit)}),
				WithOverallUsed(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tt.initial)}))
			second := first.DeepCopy()
			second.Name, second.UID = "second", "second"
			calls := map[string]int{}
			firstUpdated := ""
			injectedConflict := false
			c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(first, second).WithStatusSubresource(first).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						name := obj.GetName()
						calls[name]++
						if firstUpdated == "" {
							firstUpdated = name
						}
						if name != firstUpdated && !injectedConflict {
							injectedConflict = true
							if tt.recreate {
								replacement := &policyv1alpha1.FederatedResourceQuota{}
								require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: firstUpdated}, replacement))
								require.NoError(t, c.Delete(ctx, replacement))
								replacement.ResourceVersion, replacement.UID = "", "replacement"
								replacement.Status.OverallUsed = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tt.initial)}
								require.NoError(t, c.Create(ctx, replacement))
							}
							return apierrors.NewConflict(schema.GroupResource{Group: policyv1alpha1.GroupVersion.Group, Resource: "federatedresourcequotas"},
								name, errors.New("concurrent quota update"))
						}
						return c.SubResource(subresource).Update(ctx, obj, opts...)
					},
				}).Build()
			validator := &ValidatingAdmission{Client: c}
			binding := makeTestRB("default", "binding")
			outcome := validator.processFRQsWithRetries(t.Context(), binding,
				corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tt.delta)}, tt.dryRun)
			assert.NoError(t, validator.handleFRQOutcome(binding, outcome))
			assert.True(t, injectedConflict)
			assert.Equal(t, tt.firstCalls, calls[firstUpdated], "a committed quota delta must not be replayed")
			for _, original := range []*policyv1alpha1.FederatedResourceQuota{first, second} {
				current := &policyv1alpha1.FederatedResourceQuota{}
				require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(original), current))
				assert.True(t, current.Status.OverallUsed.Cpu().Equal(resource.MustParse(tt.expected)),
					"quota %s usage = %s, want %s", current.Name, current.Status.OverallUsed.Cpu().String(), tt.expected)
			}
		})
	}
}
