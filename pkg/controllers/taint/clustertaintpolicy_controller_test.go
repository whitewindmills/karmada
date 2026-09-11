/*
Copyright 2025 The Karmada Authors.

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
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
)

func TestClusterPredicateTargetChanges(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*clusterv1alpha1.Cluster)
		want   bool
	}{
		{name: "labels", mutate: func(c *clusterv1alpha1.Cluster) { c.Labels = map[string]string{"target": "yes"} }, want: true},
		{name: "provider", mutate: func(c *clusterv1alpha1.Cluster) { c.Spec.Provider = "new-provider" }, want: true},
		{name: "region", mutate: func(c *clusterv1alpha1.Cluster) { c.Spec.Region = "new-region" }, want: true},
		{name: "zones", mutate: func(c *clusterv1alpha1.Cluster) { c.Spec.Zones = []string{"new-zone"} }, want: true},
		{name: "conditions", mutate: func(c *clusterv1alpha1.Cluster) { c.Status.Conditions[0].Status = metav1.ConditionFalse }, want: true},
		{name: "resource version only", mutate: func(c *clusterv1alpha1.Cluster) { c.ResourceVersion = "2" }},
		{name: "annotations only", mutate: func(c *clusterv1alpha1.Cluster) { c.Annotations = map[string]string{"note": "changed"} }},
		{name: "controller taint update", mutate: func(c *clusterv1alpha1.Cluster) {
			c.Spec.Taints = []corev1.Taint{{Key: "example.com/taint", Effect: corev1.TaintEffectNoSchedule}}
		}},
		{name: "unchanged", mutate: func(*clusterv1alpha1.Cluster) {}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldCluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "member", ResourceVersion: "1"},
				Status: clusterv1alpha1.ClusterStatus{
					Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
				},
			}
			newCluster := oldCluster.DeepCopy()
			tt.mutate(newCluster)
			got := newClusterPredicate().Update(event.UpdateEvent{ObjectOld: oldCluster, ObjectNew: newCluster})
			if got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionMatches(t *testing.T) {
	tests := []struct {
		name            string
		conditions      []metav1.Condition
		matchConditions []policyv1alpha1.MatchCondition
		expected        bool
	}{
		{
			name: "empty match conditions",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "True"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{},
			expected:        false,
		},
		{
			name: "match: operator is In",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "True"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionTrue},
				},
			},
			expected: true,
		},
		{
			name: "don't match: operator is In",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "False"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionTrue},
				},
			},
			expected: false,
		},
		{
			name: "match: operator is NotIn",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "False"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpNotIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionUnknown},
				},
			},
			expected: true,
		},
		{
			name: "don't match: operator is NotIn",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "False"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpNotIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionFalse},
				},
			},
			expected: false,
		},
		{
			name: "match: multiple conditions",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "Unknown"},
				{Type: "NetworkReady", Status: "False"},
				{Type: "StorageReady", Status: "True"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown},
				},
				{
					ConditionType: "NetworkReady",
					Operator:      policyv1alpha1.MatchConditionOpNotIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionTrue},
				},
			},
			expected: true,
		},
		{
			name: "don't match: multiple conditions",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: "True"},
				{Type: "NetworkReady", Status: "False"},
				{Type: "StorageReady", Status: "True"},
			},
			matchConditions: []policyv1alpha1.MatchCondition{
				{
					ConditionType: "Ready",
					Operator:      policyv1alpha1.MatchConditionOpIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown},
				},
				{
					ConditionType: "NetworkReady",
					Operator:      policyv1alpha1.MatchConditionOpNotIn,
					StatusValues:  []metav1.ConditionStatus{metav1.ConditionTrue},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := conditionMatches(tt.conditions, tt.matchConditions)
			if result != tt.expected {
				t.Errorf("test failed: %s, expected: %v, actual: %v", tt.name, tt.expected, result)
			}
		})
	}
}

func TestAddTaintsOnCluster(t *testing.T) {
	now := metav1.Now()
	oneMinuteAgo := metav1.NewTime(now.Add(time.Duration(-1) * time.Minute))

	tests := []struct {
		name           string
		cluster        *clusterv1alpha1.Cluster
		taints         []policyv1alpha1.Taint
		now            metav1.Time
		expectedTaints []corev1.Taint
	}{
		{
			name: "add new taint",
			cluster: &clusterv1alpha1.Cluster{
				Spec: clusterv1alpha1.ClusterSpec{
					Taints: []corev1.Taint{},
				},
			},
			taints: []policyv1alpha1.Taint{
				{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule},
			},
			now: now,
			expectedTaints: []corev1.Taint{
				{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &now},
			},
		},
		{
			name: "add the existing taint, don't update TimeAdded",
			cluster: &clusterv1alpha1.Cluster{
				Spec: clusterv1alpha1.ClusterSpec{
					Taints: []corev1.Taint{
						{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &oneMinuteAgo},
					},
				},
			},
			taints: []policyv1alpha1.Taint{
				{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule},
			},
			now: now,
			expectedTaints: []corev1.Taint{
				{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &oneMinuteAgo},
			},
		},
		{
			name: "add multi taints",
			cluster: &clusterv1alpha1.Cluster{
				Spec: clusterv1alpha1.ClusterSpec{
					Taints: []corev1.Taint{
						{Key: "key2", Value: "value2", Effect: corev1.TaintEffectPreferNoSchedule, TimeAdded: &oneMinuteAgo},
					},
				},
			},
			taints: []policyv1alpha1.Taint{
				{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule},
				{Key: "key3", Value: "value3", Effect: corev1.TaintEffectNoExecute},
			},
			now: now,
			expectedTaints: []corev1.Taint{
				{Key: "key2", Value: "value2", Effect: corev1.TaintEffectPreferNoSchedule, TimeAdded: &oneMinuteAgo},
				{Key: "key1", Value: "value1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &now},
				{Key: "key3", Value: "value3", Effect: corev1.TaintEffectNoExecute, TimeAdded: &now},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := addTaintsOnCluster(tt.cluster, tt.taints, tt.now)
			if !reflect.DeepEqual(result, tt.expectedTaints) {
				t.Errorf("test failed, expected: %v, actual: %v", tt.expectedTaints, result)
			}
		})
	}
}

func TestRemoveTaintsOnCluster(t *testing.T) {
	now := metav1.Now()
	oneMinuteAgo := metav1.NewTime(now.Add(time.Duration(-1) * time.Minute))

	tests := []struct {
		name           string
		cluster        *clusterv1alpha1.Cluster
		taintsToRemove []policyv1alpha1.Taint
		expected       []corev1.Taint
	}{
		{
			name: "remove a taint",
			cluster: &clusterv1alpha1.Cluster{
				Spec: clusterv1alpha1.ClusterSpec{
					Taints: []corev1.Taint{
						{Key: "key1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &oneMinuteAgo},
					},
				},
			},
			taintsToRemove: []policyv1alpha1.Taint{
				{Key: "key1", Effect: corev1.TaintEffectNoSchedule},
			},
			expected: []corev1.Taint{},
		},
		{
			name: "remove multiple matched taints",
			cluster: &clusterv1alpha1.Cluster{
				Spec: clusterv1alpha1.ClusterSpec{
					Taints: []corev1.Taint{
						{Key: "key1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &oneMinuteAgo},
						{Key: "key2", Effect: corev1.TaintEffectNoExecute, TimeAdded: &now},
					},
				},
			},
			taintsToRemove: []policyv1alpha1.Taint{
				{Key: "key1", Effect: corev1.TaintEffectNoSchedule},
				{Key: "key2", Effect: corev1.TaintEffectNoExecute},
			},
			expected: []corev1.Taint{},
		},
		{
			name: "no matched taints",
			cluster: &clusterv1alpha1.Cluster{
				Spec: clusterv1alpha1.ClusterSpec{
					Taints: []corev1.Taint{
						{Key: "key1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &oneMinuteAgo},
					},
				},
			},
			taintsToRemove: []policyv1alpha1.Taint{
				{Key: "key2", Effect: corev1.TaintEffectNoExecute},
			},
			expected: []corev1.Taint{
				{Key: "key1", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &oneMinuteAgo},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeTaintsFromCluster(tt.cluster, tt.taintsToRemove)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("test failed, expected: %v, actual: %v", tt.expected, result)
			}
		})
	}
}
