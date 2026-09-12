/*
Copyright 2023 The Karmada Authors.

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
	"fmt"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/karmada-io/karmada/pkg/util"
	"github.com/karmada-io/karmada/pkg/util/helper"
)

type labelEventKind int

const (
	// addLabelEvent refer to adding util.RetainReplicasLabel to resource scaled by HPA
	addLabelEvent labelEventKind = iota
	// deleteLabelEvent refer to deleting util.RetainReplicasLabel from resource scaled by HPA
	deleteLabelEvent
)

type labelEvent struct {
	kind           labelEventKind
	hpa            types.NamespacedName
	uid            types.UID
	scaleTargetRef autoscalingv2.CrossVersionObjectReference
}

func newLabelEvent(kind labelEventKind, hpa *autoscalingv2.HorizontalPodAutoscaler) labelEvent {
	return labelEvent{
		kind:           kind,
		hpa:            types.NamespacedName{Namespace: hpa.Namespace, Name: hpa.Name},
		uid:            hpa.UID,
		scaleTargetRef: hpa.Spec.ScaleTargetRef,
	}
}

func (r *HpaScaleTargetMarker) reconcileScaleRef(key util.QueueKey) (err error) {
	event, ok := key.(labelEvent)
	if !ok {
		err = errors.New("invalid key")
		klog.ErrorS(err, "reconcile hpa scale ref failed on type assertion", "key", key)
		return err
	}

	switch event.kind {
	case addLabelEvent:
		err = r.addHPALabelToScaleRef(context.TODO(), event)
	case deleteLabelEvent:
		err = r.deleteHPALabelFromScaleRef(context.TODO(), event)
	default:
		err = errors.New("invalid label event")
		klog.ErrorS(err, "reconcile hpa scale ref failed", "key", key)
		return err
	}

	if err != nil {
		klog.ErrorS(err, "reconcile hpa scale ref failed")
	}
	return err
}

func (r *HpaScaleTargetMarker) addHPALabelToScaleRef(ctx context.Context, event labelEvent) error {
	hpa := event.hpa
	// Retries can outlive the HPA or its original scale target.
	currentHPA := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := r.hpaReader.Get(ctx, hpa, currentHPA); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(4).InfoS("HPA no longer exists, skip label event", "namespace", hpa.Namespace, "name", hpa.Name)
			return nil
		}
		return fmt.Errorf("failed to get current HPA (%s/%s): %w", hpa.Namespace, hpa.Name, err)
	}
	if currentHPA.UID != event.uid || currentHPA.Spec.ScaleTargetRef != event.scaleTargetRef ||
		!currentHPA.DeletionTimestamp.IsZero() || !hasBeenPropagated(currentHPA) {
		klog.V(4).InfoS("skip obsolete HPA label event", "namespace", hpa.Namespace, "name", hpa.Name)
		return nil
	}

	targetGVK := schema.FromAPIVersionAndKind(event.scaleTargetRef.APIVersion, event.scaleTargetRef.Kind)
	mapping, err := r.RESTMapper.RESTMapping(targetGVK.GroupKind(), targetGVK.Version)
	if err != nil {
		return fmt.Errorf("unable to recognize scale ref resource, %s/%v, err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}

	scaleRef, err := r.DynamicClient.Resource(mapping.Resource).Namespace(hpa.Namespace).Get(ctx, event.scaleTargetRef.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.InfoS("scale ref resource is not found, skip processing", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
			return nil
		}
		return fmt.Errorf("failed to find scale ref resource (%s/%v), err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}

	// use patch is better than update, when modification occur after get, patch can still success while update can not
	newScaleRef := scaleRef.DeepCopy()
	util.MergeLabel(newScaleRef, util.RetainReplicasLabel, util.RetainReplicasValue)
	patchBytes, err := helper.GenMergePatch(scaleRef, newScaleRef)
	if err != nil {
		return fmt.Errorf("failed to gen merge patch (%s/%v), err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}
	if len(patchBytes) == 0 {
		klog.InfoS("hpa labels already exist, skip adding", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
		return nil
	}

	_, err = r.DynamicClient.Resource(mapping.Resource).Namespace(newScaleRef.GetNamespace()).
		Patch(ctx, newScaleRef.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.InfoS("scale ref resource is not found, skip processing", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
			return nil
		}
		return fmt.Errorf("failed to patch scale ref resource (%s/%v), err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}

	klog.InfoS("add hpa labels to scale ref success", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
	return nil
}

func (r *HpaScaleTargetMarker) deleteHPALabelFromScaleRef(ctx context.Context, event labelEvent) error {
	hpa := event.hpa
	owners := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := r.hpaReader.List(ctx, owners, client.InNamespace(hpa.Namespace), client.MatchingFields{
		hpaScaleTargetIndex: hpaScaleTargetKey(event.scaleTargetRef),
	}); err != nil {
		return fmt.Errorf("failed to find HPAs targeting (%s/%v): %w", hpa.Namespace, event.scaleTargetRef, err)
	}
	for i := range owners.Items {
		if hasBeenPropagated(&owners.Items[i]) {
			klog.V(4).InfoS("scale target still has a propagated HPA, skip removing retention",
				"namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef, "hpa", owners.Items[i].Name)
			return nil
		}
	}

	targetGVK := schema.FromAPIVersionAndKind(event.scaleTargetRef.APIVersion, event.scaleTargetRef.Kind)
	mapping, err := r.RESTMapper.RESTMapping(targetGVK.GroupKind(), targetGVK.Version)
	if err != nil {
		return fmt.Errorf("unable to recognize scale ref resource, %s/%v, err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}

	scaleRef, err := r.DynamicClient.Resource(mapping.Resource).Namespace(hpa.Namespace).Get(ctx, event.scaleTargetRef.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.InfoS("scale ref resource is not found, skip processing", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
			return nil
		}
		return fmt.Errorf("failed to find scale ref resource (%s/%v), err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}

	// use patch is better than update, when modification occur after get, patch can still success while update can not
	newScaleRef := scaleRef.DeepCopy()
	util.RemoveLabels(newScaleRef, util.RetainReplicasLabel)
	patchBytes, err := helper.GenMergePatch(scaleRef, newScaleRef)
	if err != nil {
		return fmt.Errorf("failed to gen merge patch (%s/%v), err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}
	if len(patchBytes) == 0 {
		klog.InfoS("hpa labels not exist, skip deleting", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
		return nil
	}

	_, err = r.DynamicClient.Resource(mapping.Resource).Namespace(newScaleRef.GetNamespace()).
		Patch(ctx, newScaleRef.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.InfoS("scale ref resource is not found, skip processing", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
			return nil
		}
		return fmt.Errorf("failed to patch scale ref resource (%s/%v), err: %+v", hpa.Namespace, event.scaleTargetRef, err)
	}

	klog.InfoS("delete hpa labels from scale ref success", "namespace", hpa.Namespace, "scaleTargetRef", event.scaleTargetRef)
	return nil
}
