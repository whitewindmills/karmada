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

package hpascaletargetmarker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util"
)

type recordingLabelWorker struct {
	util.AsyncPriorityWorker
	items []any
}

func (w *recordingLabelWorker) Add(item any) {
	w.items = append(w.items, item)
}

func TestUpdatePropagationClaimTransitions(t *testing.T) {
	pp := policyv1alpha1.PropagationPolicyPermanentIDLabel
	cpp := policyv1alpha1.ClusterPropagationPolicyPermanentIDLabel
	tests := []struct {
		name          string
		oldLabels     map[string]string
		newLabels     map[string]string
		initialRetain bool
		wantRetain    bool
		wantEvents    []labelEventKind
	}{
		{
			name: "PP claim removed", oldLabels: map[string]string{pp: "policy"},
			initialRetain: true, wantEvents: []labelEventKind{deleteLabelEvent},
		},
		{
			name: "CPP claim removed", oldLabels: map[string]string{cpp: "policy"},
			initialRetain: true, wantEvents: []labelEventKind{deleteLabelEvent},
		},
		{
			name: "PP to CPP transfer", oldLabels: map[string]string{pp: "old"}, newLabels: map[string]string{cpp: "new"},
			initialRetain: true, wantRetain: true, wantEvents: []labelEventKind{addLabelEvent},
		},
		{
			name: "one of two claims remains", oldLabels: map[string]string{pp: "old", cpp: "other"}, newLabels: map[string]string{cpp: "other"},
			initialRetain: true, wantRetain: true, wantEvents: []labelEventKind{addLabelEvent},
		},
		{
			name:          "unclaimed HPA leaves unrelated retention unchanged",
			initialRetain: true, wantRetain: true,
		},
		{
			name: "claim acquired", newLabels: map[string]string{pp: "policy"},
			wantRetain: true, wantEvents: []labelEventKind{addLabelEvent},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldHPA := propagatedHPA()
			oldHPA.Labels = tt.oldLabels
			current := oldHPA.DeepCopy()
			current.Labels, current.ResourceVersion = tt.newLabels, "2"
			marker, _, workloadClient := newTestMarker(t, current)
			if tt.initialRetain {
				retainTestTarget(t, workloadClient)
			}
			worker := &recordingLabelWorker{}
			marker.scaleTargetWorker = worker
			marker.Update(event.UpdateEvent{ObjectOld: oldHPA, ObjectNew: current})

			var kinds []labelEventKind
			for _, item := range worker.items {
				label, ok := item.(labelEvent)
				require.True(t, ok)
				kinds = append(kinds, label.kind)
				require.NoError(t, marker.reconcileScaleRef(item))
			}
			target, err := workloadClient.Resource(deploymentGVR).Namespace("default").Get(t.Context(), "target", metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tt.wantRetain, target.GetLabels()[util.RetainReplicasLabel] == util.RetainReplicasValue)
			require.Equal(t, tt.wantEvents, kinds)
			require.Equal(t, "preserved", target.GetLabels()["user"])
		})
	}
}

func TestLabelQueueDeduplicatesEquivalentEvents(t *testing.T) {
	tests := []struct {
		name    string
		enqueue func(*HpaScaleTargetMarker)
		want    int32
	}{
		{
			name: "one hundred status-only updates",
			enqueue: func(marker *HpaScaleTargetMarker) {
				oldHPA := propagatedHPA()
				for i := range 100 {
					current := oldHPA.DeepCopy()
					current.ResourceVersion = fmt.Sprint(i + 2)
					current.Status.DesiredReplicas = int32(i%2 + 1)
					marker.Update(event.UpdateEvent{ObjectOld: oldHPA, ObjectNew: current})
					oldHPA = current
				}
			},
			want: 1,
		},
		{
			name: "replacement HPA identity remains distinct",
			enqueue: func(marker *HpaScaleTargetMarker) {
				oldHPA := propagatedHPA()
				current := oldHPA.DeepCopy()
				current.UID = "replacement-hpa"
				marker.Create(event.CreateEvent{Object: oldHPA})
				marker.Create(event.CreateEvent{Object: current})
			},
			want: 2,
		},
		{
			name: "addition and retarget cleanup remain distinct",
			enqueue: func(marker *HpaScaleTargetMarker) {
				oldHPA := propagatedHPA()
				current := oldHPA.DeepCopy()
				current.Spec.ScaleTargetRef.Name = "other-target"
				marker.Create(event.CreateEvent{Object: oldHPA})
				marker.Update(event.UpdateEvent{ObjectOld: oldHPA, ObjectNew: current})
			},
			want: 3,
		},
		{
			name: "target API version changes remain distinct",
			enqueue: func(marker *HpaScaleTargetMarker) {
				oldHPA := propagatedHPA()
				oldHPA.Spec.ScaleTargetRef.APIVersion = "example.com/v1"
				oldHPA.Spec.ScaleTargetRef.Kind = "Worker"
				current := oldHPA.DeepCopy()
				current.Spec.ScaleTargetRef.APIVersion = "example.com/v2"
				marker.Create(event.CreateEvent{Object: oldHPA})
				marker.Update(event.UpdateEvent{ObjectOld: oldHPA, ObjectNew: current})
			},
			want: 3,
		},
	}
	for _, priority := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("priority=%t/%s", priority, tt.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var processed atomic.Int32
					worker := util.NewAsyncWorker(util.Options{
						UsePriorityQueue: priority,
						ReconcileFunc: func(util.QueueKey) error {
							processed.Add(1)
							return nil
						},
					})
					marker := &HpaScaleTargetMarker{scaleTargetWorker: worker}
					tt.enqueue(marker)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					worker.Run(ctx, 1)
					synctest.Wait()
					require.Equal(t, tt.want, processed.Load())
					cancel()
					synctest.Wait()
				})
			})
		}
	}
}
