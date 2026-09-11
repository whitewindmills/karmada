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

package controlplane_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientset "k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	coretesting "k8s.io/client-go/testing"

	operatorv1alpha1 "github.com/karmada-io/karmada/operator/pkg/apis/operator/v1alpha1"
	"github.com/karmada-io/karmada/operator/pkg/controlplane/apiserver"
	"github.com/karmada-io/karmada/operator/pkg/controlplane/metricsadapter"
	"github.com/karmada-io/karmada/operator/pkg/controlplane/search"
	"github.com/karmada-io/karmada/operator/pkg/controlplane/webhook"
	"github.com/karmada-io/karmada/operator/pkg/util"
)

func TestComponentDeploymentFailuresPreserveCause(t *testing.T) {
	const name, namespace = "demo", "test"
	common := operatorv1alpha1.CommonSettings{
		Replicas: new(int32(1)), ImagePullPolicy: corev1.PullIfNotPresent,
		Image: operatorv1alpha1.Image{ImageRepository: "registry.example/component", ImageTag: "v1"},
	}
	etcdCfg := &operatorv1alpha1.Etcd{Local: &operatorv1alpha1.LocalEtcd{}}
	components := &operatorv1alpha1.KarmadaComponents{
		Etcd: etcdCfg,
		KarmadaAPIServer: &operatorv1alpha1.KarmadaAPIServer{
			CommonSettings: common, ServiceSubnet: new("10.96.0.0/12"),
		},
		KarmadaAggregatedAPIServer: &operatorv1alpha1.KarmadaAggregatedAPIServer{CommonSettings: common},
	}
	for _, tt := range []struct {
		deploymentName string
		ensure         func(clientset.Interface) error
	}{
		{
			deploymentName: util.KarmadaAPIServerName(name),
			ensure: func(c clientset.Interface) error {
				return apiserver.EnsureKarmadaAPIServer(c, components, name, namespace, nil)
			},
		},
		{
			deploymentName: util.KarmadaAggregatedAPIServerName(name),
			ensure: func(c clientset.Interface) error {
				return apiserver.EnsureKarmadaAggregatedAPIServer(c, components, name, namespace, nil)
			},
		},
		{
			deploymentName: util.KarmadaSearchName(name),
			ensure: func(c clientset.Interface) error {
				return search.EnsureKarmadaSearch(c, &operatorv1alpha1.KarmadaSearch{CommonSettings: common}, etcdCfg, name, namespace, nil)
			},
		},
		{
			deploymentName: util.KarmadaMetricsAdapterName(name),
			ensure: func(c clientset.Interface) error {
				return metricsadapter.EnsureKarmadaMetricAdapter(c, &operatorv1alpha1.KarmadaMetricsAdapter{CommonSettings: common}, name, namespace)
			},
		},
		{
			deploymentName: util.KarmadaWebhookName(name),
			ensure: func(c clientset.Interface) error {
				return webhook.EnsureKarmadaWebhook(c, &operatorv1alpha1.KarmadaWebhook{CommonSettings: common}, name, namespace, nil)
			},
		},
	} {
		for _, verb := range []string{"create", "get", "update"} {
			t.Run(tt.deploymentName+"/"+verb, func(t *testing.T) {
				var objects []runtime.Object
				if verb != "create" {
					objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
						Name: tt.deploymentName, Namespace: namespace, ResourceVersion: "1",
					}})
				}
				c := fakeclientset.NewClientset(objects...)
				cause := apierrors.NewInvalid(appsv1.SchemeGroupVersion.WithKind("Deployment").GroupKind(), tt.deploymentName,
					field.ErrorList{field.Invalid(field.NewPath("spec", "template", "metadata", "labels"), "invalid,label", "invalid label value")})
				if verb == "get" {
					cause = apierrors.NewForbidden(appsv1.SchemeGroupVersion.WithResource("deployments").GroupResource(),
						tt.deploymentName, errors.New("access denied"))
				}
				failed := false
				c.PrependReactor(verb, "deployments", func(coretesting.Action) (bool, runtime.Object, error) {
					failed = true
					return true, nil, cause
				})
				var err error
				if !assert.NotPanics(t, func() { err = tt.ensure(c) }) {
					return
				}
				assert.True(t, failed, "installer must reach the failing API operation")
				assert.ErrorIs(t, err, cause)
				assert.ErrorContains(t, err, tt.deploymentName)
				for _, action := range c.Actions() {
					assert.Equal(t, "deployments", action.GetResource().Resource,
						"failed installation must not proceed to Services or PDBs")
				}
			})
		}
	}
}
