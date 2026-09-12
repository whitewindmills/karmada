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
	"testing"

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
