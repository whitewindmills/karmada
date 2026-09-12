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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	kubetesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util"
)

var deploymentGVR = appsv1.SchemeGroupVersion.WithResource("deployments")

func propagatedHPA() *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "scaler",
			Namespace:       "default",
			UID:             "original-hpa",
			ResourceVersion: "1",
			Labels:          map[string]string{policyv1alpha1.PropagationPolicyPermanentIDLabel: "policy-uid"},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas: 10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: appsv1.SchemeGroupVersion.String(),
				Kind:       util.DeploymentKind,
				Name:       "target",
			},
		},
	}
}

func newTestMarker(t *testing.T, currentHPA *autoscalingv2.HorizontalPodAutoscaler) (*HpaScaleTargetMarker, client.WithWatch, *fakedynamic.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, autoscalingv2.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if currentHPA != nil {
		builder.WithObjects(currentHPA)
	}
	hpaClient := builder.Build()
	workloadClient := fakedynamic.NewSimpleDynamicClient(scheme, &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: util.DeploymentKind},
		ObjectMeta: metav1.ObjectMeta{
			Name: "target", Namespace: "default", Labels: map[string]string{"user": "preserved"},
		},
	})
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{appsv1.SchemeGroupVersion})
	mapper.Add(appsv1.SchemeGroupVersion.WithKind(util.DeploymentKind), meta.RESTScopeNamespace)
	return &HpaScaleTargetMarker{DynamicClient: workloadClient, RESTMapper: mapper, hpaReader: hpaClient}, hpaClient, workloadClient
}

func TestAddHPALabelIgnoresObsoleteEvents(t *testing.T) {
	tests := []struct {
		name      string
		deleted   bool
		mutate    func(*autoscalingv2.HorizontalPodAutoscaler)
		wantLabel bool
	}{
		{name: "HPA deleted", deleted: true},
		{name: "HPA recreated", mutate: func(hpa *autoscalingv2.HorizontalPodAutoscaler) { hpa.UID = "replacement-hpa" }},
		{name: "HPA retargeted", mutate: func(hpa *autoscalingv2.HorizontalPodAutoscaler) { hpa.Spec.ScaleTargetRef.Name = "other-target" }},
		{name: "HPA no longer propagated", mutate: func(hpa *autoscalingv2.HorizontalPodAutoscaler) { hpa.Labels = nil }},
		{
			name: "HPA deleting",
			mutate: func(hpa *autoscalingv2.HorizontalPodAutoscaler) {
				now := metav1.Now()
				hpa.DeletionTimestamp = &now
				hpa.Finalizers = []string{"example.com/finalizer"}
			},
		},
		{
			name: "newer status of the same active HPA",
			mutate: func(hpa *autoscalingv2.HorizontalPodAutoscaler) {
				hpa.ResourceVersion = "2"
				hpa.Status.DesiredReplicas = 6
			},
			wantLabel: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queued := propagatedHPA()
			current := queued.DeepCopy()
			if tt.deleted {
				current = nil
			} else if tt.mutate != nil {
				tt.mutate(current)
			}
			marker, _, workloadClient := newTestMarker(t, current)
			require.NoError(t, marker.reconcileScaleRef(labelEvent{kind: addLabelEvent, hpa: queued}))
			target, err := workloadClient.Resource(deploymentGVR).Namespace(queued.Namespace).Get(t.Context(), "target", metav1.GetOptions{})
			require.NoError(t, err)
			if tt.wantLabel {
				require.Equal(t, util.RetainReplicasValue, target.GetLabels()[util.RetainReplicasLabel])
			} else {
				require.NotContains(t, target.GetLabels(), util.RetainReplicasLabel)
			}
			require.Equal(t, "preserved", target.GetLabels()["user"])
		})
	}
}

func TestFailedAddDoesNotRestoreLabelAfterHPADeletion(t *testing.T) {
	hpa := propagatedHPA()
	marker, hpaClient, workloadClient := newTestMarker(t, hpa)
	addEvent := labelEvent{kind: addLabelEvent, hpa: hpa.DeepCopy()}
	require.NoError(t, marker.reconcileScaleRef(addEvent))

	failNextGet := true
	workloadClient.PrependReactor("get", "deployments", func(kubetesting.Action) (bool, runtime.Object, error) {
		if failNextGet {
			failNextGet = false
			return true, nil, errors.New("transient workload read failure")
		}
		return false, nil, nil
	})
	require.Error(t, marker.reconcileScaleRef(addEvent))
	require.NoError(t, hpaClient.Delete(t.Context(), hpa))
	require.NoError(t, marker.reconcileScaleRef(labelEvent{kind: deleteLabelEvent, hpa: hpa}))
	require.NoError(t, marker.reconcileScaleRef(addEvent))

	target, err := workloadClient.Resource(deploymentGVR).Namespace(hpa.Namespace).Get(t.Context(), "target", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, target.GetLabels(), util.RetainReplicasLabel)
	require.Equal(t, "preserved", target.GetLabels()["user"])
}

func TestAddHPALabelRetriesCurrentHPAReadErrors(t *testing.T) {
	hpa := propagatedHPA()
	marker, hpaClient, workloadClient := newTestMarker(t, hpa)
	readErr := errors.New("current HPA read failed")
	marker.hpaReader = interceptor.NewClient(hpaClient, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return readErr
		},
	})

	require.ErrorIs(t, marker.reconcileScaleRef(labelEvent{kind: addLabelEvent, hpa: hpa}), readErr)
	require.Empty(t, workloadClient.Actions(), "a failed HPA lookup must not mutate its scale target")
}
