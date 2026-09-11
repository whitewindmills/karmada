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

package taint

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/gclient"
)

func TestReconcilePreservesUnchangedTaints(t *testing.T) {
	for _, tt := range []struct {
		name         string
		initialTaint bool
		removeLast   bool
		changedValue bool
		wantPatches  int
	}{
		{name: "retain an existing taint lifetime", initialTaint: true},
		{name: "empty final taints are unchanged", removeLast: true},
		{name: "a new taint value gets a new lifetime", initialTaint: true, changedValue: true, wantPatches: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldTime := metav1.NewTime(time.Unix(1000, 0))
			cluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "member"},
				Status: clusterv1alpha1.ClusterStatus{
					Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
				},
			}
			if tt.initialTaint {
				cluster.Spec.Taints = []corev1.Taint{{Key: "testing/stable", Value: "same", Effect: corev1.TaintEffectNoExecute, TimeAdded: &oldTime}}
			}
			condition := []policyv1alpha1.MatchCondition{{
				ConditionType: "Ready", Operator: policyv1alpha1.MatchConditionOpIn,
				StatusValues: []metav1.ConditionStatus{metav1.ConditionTrue},
			}}
			addPolicy := &policyv1alpha1.ClusterTaintPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "z-add", CreationTimestamp: oldTime},
				Spec: policyv1alpha1.ClusterTaintPolicySpec{
					Taints:          []policyv1alpha1.Taint{{Key: "testing/stable", Value: "same", Effect: corev1.TaintEffectNoExecute}},
					AddOnConditions: condition,
				},
			}
			removePolicy := addPolicy.DeepCopy()
			removePolicy.Name = "a-remove"
			removePolicy.Spec.AddOnConditions = nil
			removePolicy.Spec.RemoveOnConditions = condition
			if tt.removeLast {
				addPolicy.Name, removePolicy.Name = "a-add", "z-remove"
			}
			if tt.changedValue {
				addPolicy.Spec.Taints[0].Value = "changed"
			}
			patches := 0
			fakeClient := fake.NewClientBuilder().WithScheme(gclient.NewSchema()).WithObjects(cluster, addPolicy, removePolicy).
				WithInterceptorFuncs(interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						patches++
						return c.Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			controller := &ClusterTaintPolicyController{Client: fakeClient, EventRecorder: record.NewFakeRecorder(10)}
			for range 2 {
				if _, err := controller.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: client.ObjectKey{Name: cluster.Name}}); err != nil {
					t.Fatal(err)
				}
			}
			if patches != tt.wantPatches {
				t.Errorf("patch requests = %d, want %d", patches, tt.wantPatches)
			}
			got := &clusterv1alpha1.Cluster{}
			if err := fakeClient.Get(t.Context(), client.ObjectKey{Name: cluster.Name}, got); err != nil {
				t.Fatal(err)
			}
			if tt.removeLast {
				if len(got.Spec.Taints) != 0 {
					t.Errorf("unexpected final taints: %v", got.Spec.Taints)
				}
				return
			}
			if len(got.Spec.Taints) != 1 {
				t.Fatalf("expected one final taint, got %v", got.Spec.Taints)
			}
			preserved := got.Spec.Taints[0].TimeAdded.Equal(&oldTime)
			if preserved == tt.changedValue {
				t.Errorf("old taint lifetime preserved = %v, changed value = %v", preserved, tt.changedValue)
			}
		})
	}
}

func TestReconcilePolicyOrderIsDeterministic(t *testing.T) {
	for _, tt := range []struct {
		name           string
		addPolicyNewer bool
		wantTaint      bool
	}{
		{name: "equal timestamps are ordered by name"},
		{name: "creation time remains primary", addPolicyNewer: true, wantTaint: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := metav1.NewTime(time.Unix(1000, 0))
			condition := []policyv1alpha1.MatchCondition{{
				ConditionType: "Ready", Operator: policyv1alpha1.MatchConditionOpIn,
				StatusValues: []metav1.ConditionStatus{metav1.ConditionTrue},
			}}
			addPolicy := policyv1alpha1.ClusterTaintPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "a-add", CreationTimestamp: now},
				Spec: policyv1alpha1.ClusterTaintPolicySpec{
					Taints:          []policyv1alpha1.Taint{{Key: "testing/ordered", Effect: corev1.TaintEffectNoSchedule}},
					AddOnConditions: condition,
				},
			}
			removePolicy := *addPolicy.DeepCopy()
			removePolicy.Name = "z-remove"
			removePolicy.Spec.AddOnConditions = nil
			removePolicy.Spec.RemoveOnConditions = condition
			if tt.addPolicyNewer {
				addPolicy.CreationTimestamp = metav1.NewTime(now.Add(time.Second))
			}
			cluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "member"},
				Status: clusterv1alpha1.ClusterStatus{
					Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
				},
			}
			reversed := false
			fakeClient := fake.NewClientBuilder().WithScheme(gclient.NewSchema()).WithObjects(cluster).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
						policies := list.(*policyv1alpha1.ClusterTaintPolicyList)
						policies.Items = []policyv1alpha1.ClusterTaintPolicy{addPolicy, removePolicy}
						if reversed {
							policies.Items[0], policies.Items[1] = policies.Items[1], policies.Items[0]
						}
						return nil
					},
				}).Build()
			controller := &ClusterTaintPolicyController{Client: fakeClient, EventRecorder: record.NewFakeRecorder(10)}
			for _, order := range []bool{false, true, false} {
				reversed = order
				if _, err := controller.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: client.ObjectKey{Name: cluster.Name}}); err != nil {
					t.Fatal(err)
				}
				got := &clusterv1alpha1.Cluster{}
				if err := fakeClient.Get(t.Context(), client.ObjectKey{Name: cluster.Name}, got); err != nil {
					t.Fatal(err)
				}
				if (len(got.Spec.Taints) > 0) != tt.wantTaint {
					t.Errorf("reversed=%v: taints = %v, want taint present %v", order, got.Spec.Taints, tt.wantTaint)
				}
			}
		})
	}
}

// TestReconcilePatchRejectsStaleData verifies that ClusterTaintPolicyController's
// Patch uses optimistic locking so that a concurrent taint modification (e.g. by
// cluster-controller adding a health taint) causes a conflict error instead of
// silently overwriting the taints array.
func TestReconcilePatchRejectsStaleData(t *testing.T) {
	ctx := context.Background()
	now := metav1.Now()

	cluster := &clusterv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "member1",
		},
		Spec: clusterv1alpha1.ClusterSpec{
			SyncMode: clusterv1alpha1.Push,
		},
		Status: clusterv1alpha1.ClusterStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					LastTransitionTime: now,
					Reason:             "ClusterNotReady",
				},
			},
		},
	}

	policy := &policyv1alpha1.ClusterTaintPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-policy",
			CreationTimestamp: now,
		},
		Spec: policyv1alpha1.ClusterTaintPolicySpec{
			Taints: []policyv1alpha1.Taint{
				{
					Key:    "custom/unhealthy",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			AddOnConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionFalse},
				},
			},
		},
	}

	// Inject a concurrent taint modification between the controller's Get and Patch.
	var mu sync.Mutex
	getCalled := false

	interceptFuncs := interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			mu.Lock()
			alreadyCalled := getCalled
			mu.Unlock()

			if alreadyCalled {
				// Simulate cluster-controller adding NotReady taint between Get and Patch.
				clusterObj := &clusterv1alpha1.Cluster{}
				if err := c.Get(ctx, types.NamespacedName{Name: "member1"}, clusterObj); err == nil {
					clusterObj.Spec.Taints = append(clusterObj.Spec.Taints, corev1.Taint{
						Key:    "cluster.karmada.io/not-ready",
						Effect: corev1.TaintEffectNoSchedule,
					})
					_ = c.Update(ctx, clusterObj)
				}
			}

			return c.Patch(ctx, obj, patch, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if key.Name == "member1" {
				mu.Lock()
				getCalled = true
				mu.Unlock()
			}
			return err
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(gclient.NewSchema()).
		WithObjects(cluster, policy).
		WithStatusSubresource(&clusterv1alpha1.Cluster{}).
		WithInterceptorFuncs(interceptFuncs).
		Build()

	controller := &ClusterTaintPolicyController{
		Client:        fakeClient,
		EventRecorder: record.NewFakeRecorder(1024),
	}

	_, err := controller.Reconcile(ctx, controllerruntime.Request{
		NamespacedName: types.NamespacedName{Name: "member1"},
	})

	// With optimistic locking the patch must fail with a conflict error,
	// which controller-runtime will requeue automatically.
	if err == nil {
		t.Fatal("expected conflict error from optimistic lock, but Reconcile succeeded — " +
			"this means MergeFrom is not using optimistic locking and concurrent taint changes can be silently lost")
	}
	t.Logf("Reconcile correctly returned conflict error: %v", err)
}
