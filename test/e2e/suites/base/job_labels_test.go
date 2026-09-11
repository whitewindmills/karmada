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

package base

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"github.com/karmada-io/karmada/test/e2e/framework"
	testhelper "github.com/karmada-io/karmada/test/helper"
)

var _ = ginkgo.Describe("[Job] suspended Pod template label propagation", func() {
	ginkgo.It("preserves member identity labels while applying user label changes", func(ctx context.Context) {
		job := testhelper.NewJob(testNamespace, jobNamePrefix+rand.String(RandomStrLength))
		job.Spec.Suspend = new(true)
		job.Spec.Template.Labels = map[string]string{"change": "old", "remove": "old"}
		framework.CreateJob(kubeClient, job)
		ginkgo.DeferCleanup(func() { framework.RemoveJob(kubeClient, job.Namespace, job.Name) })

		policy := testhelper.NewPropagationPolicy(job.Namespace, job.Name, []policyv1alpha1.ResourceSelector{{
			APIVersion: job.APIVersion, Kind: job.Kind, Name: job.Name,
		}}, policyv1alpha1.Placement{ClusterAffinity: &policyv1alpha1.ClusterAffinity{ClusterNames: framework.ClusterNames()}})
		framework.CreatePropagationPolicy(karmadaClient, policy)
		ginkgo.DeferCleanup(func() { framework.RemovePropagationPolicy(karmadaClient, policy.Namespace, policy.Name) })

		framework.WaitJobPresentOnClustersFitWith(framework.ClusterNames(), job.Namespace, job.Name, func(member *batchv1.Job) bool {
			return member.Spec.Suspend != nil && *member.Spec.Suspend && member.Spec.Template.Labels["change"] == "old"
		})
		ginkgo.By("changing, adding and removing user labels while the Job remains suspended")
		_, err := kubeClient.BatchV1().Jobs(job.Namespace).Patch(ctx, job.Name, types.MergePatchType,
			[]byte(`{"spec":{"template":{"metadata":{"labels":{"change":"new","added":"new","remove":null}}}}}`), metav1.PatchOptions{})
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		framework.WaitJobPresentOnClustersFitWith(framework.ClusterNames(), job.Namespace, job.Name, func(member *batchv1.Job) bool {
			labels := member.Spec.Template.Labels
			_, removed := labels["remove"]
			return labels["change"] == "new" && labels["added"] == "new" && !removed &&
				labels[batchv1.ControllerUidLabel] == string(member.UID) && labels[batchv1.JobNameLabel] == member.Name
		})
	})
})
