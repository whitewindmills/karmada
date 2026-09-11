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

package cronfederatedhpa

import (
	"context"
	"errors"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	autoscalingv1alpha1 "github.com/karmada-io/karmada/pkg/apis/autoscaling/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/gclient"
	"github.com/karmada-io/karmada/pkg/util/helper"
)

func TestReconcileReplacesExecutorsOnlyOnTargetChange(t *testing.T) {
	rule := autoscalingv1alpha1.CronFederatedHPARule{
		Name: "scale", Schedule: "0 0 1 1 *", TargetReplicas: new(int32(3)),
	}
	cron := &autoscalingv1alpha1.CronFederatedHPA{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: autoscalingv1alpha1.CronFederatedHPASpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "first"},
			Rules:          []autoscalingv1alpha1.CronFederatedHPARule{rule},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(gclient.NewSchema()).WithObjects(cron).WithStatusSubresource(cron).Build()
	recorder := record.NewFakeRecorder(20)
	handler := NewCronHandler(fakeClient, recorder)
	key := helper.GetCronFederatedHPAKey(cron)
	defer handler.StopCronFHPAExecutor(key)
	controller := &CronFHPAController{Client: fakeClient, EventRecorder: recorder, CronHandler: handler}
	request := controllerruntime.Request{NamespacedName: client.ObjectKeyFromObject(cron)}
	var previous RuleCron
	previousTarget := ""
	for _, target := range []string{"first", "first", "second", "second", "first", "first"} {
		if err := fakeClient.Get(t.Context(), request.NamespacedName, cron); err != nil {
			t.Fatal(err)
		}
		cron.Spec.ScaleTargetRef.Name = target
		if err := fakeClient.Update(t.Context(), cron); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Reconcile(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		handler.executorLock.RLock()
		current, exists := handler.cronExecutorMap[key][rule.Name]
		handler.executorLock.RUnlock()
		if !exists {
			t.Fatal("rule executor was not registered")
		}
		if previous.Scheduler != nil {
			changed := target != previousTarget
			replaced := current.Scheduler != previous.Scheduler
			if replaced != changed {
				t.Errorf("target %q -> %q: replaced executor = %v, want %v", previousTarget, target, replaced, changed)
			}
			if changed && previous.Scheduler.IsRunning() {
				t.Error("previous target's executor is still running")
			}
		}
		previous, previousTarget = current, target
	}
}

func TestReconcileRollsBackExecutorOnHistoryFailure(t *testing.T) {
	for _, removeRule := range []bool{false, true} {
		name := "retry persists history"
		if removeRule {
			name = "rule removed before retry cannot remain active"
		}
		t.Run(name, func(t *testing.T) {
			rule := autoscalingv1alpha1.CronFederatedHPARule{
				Name: "scale", Schedule: "0 0 1 1 *", TargetReplicas: new(int32(3)),
			}
			cron := &autoscalingv1alpha1.CronFederatedHPA{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: autoscalingv1alpha1.CronFederatedHPASpec{
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "workload"},
					Rules:          []autoscalingv1alpha1.CronFederatedHPARule{rule},
				},
			}
			statusError := errors.New("status update failed")
			statusWrites := 0
			fakeClient := fake.NewClientBuilder().WithScheme(gclient.NewSchema()).WithObjects(cron).WithStatusSubresource(cron).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						statusWrites++
						if statusWrites == 1 {
							return statusError
						}
						return c.SubResource(subresource).Update(ctx, obj, opts...)
					},
				}).Build()
			recorder := record.NewFakeRecorder(10)
			handler := NewCronHandler(fakeClient, recorder)
			key := helper.GetCronFederatedHPAKey(cron)
			defer handler.StopCronFHPAExecutor(key)
			controller := &CronFHPAController{Client: fakeClient, EventRecorder: recorder, CronHandler: handler}
			request := controllerruntime.Request{NamespacedName: client.ObjectKeyFromObject(cron)}
			if _, err := controller.Reconcile(t.Context(), request); !errors.Is(err, statusError) {
				t.Fatalf("first reconciliation error = %v, want status failure", err)
			}
			if _, exists := handler.RuleCronExecutorExists(key, rule.Name); exists {
				t.Error("executor remained active without persisted execution history")
			}
			if removeRule {
				cron.Spec.Rules = nil
				if err := fakeClient.Update(t.Context(), cron); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := controller.Reconcile(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			_, exists := handler.RuleCronExecutorExists(key, rule.Name)
			if exists == removeRule {
				t.Errorf("executor exists = %v after retry, rule removed = %v", exists, removeRule)
			}
			latest := &autoscalingv1alpha1.CronFederatedHPA{}
			if err := fakeClient.Get(t.Context(), request.NamespacedName, latest); err != nil {
				t.Fatal(err)
			}
			if !removeRule && (len(latest.Status.ExecutionHistories) != 1 || latest.Status.ExecutionHistories[0].NextExecutionTime == nil) {
				t.Errorf("retry did not persist the rule history: %+v", latest.Status.ExecutionHistories)
			}
		})
	}
}
