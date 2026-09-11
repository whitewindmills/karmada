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

package helper

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
)

func Test_BuildPreservedLabelState(t *testing.T) {
	type args struct {
		statePreservation *policyv1alpha1.StatePreservation
		rawStatus         []byte
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "successful case",
			args: args{
				statePreservation: &policyv1alpha1.StatePreservation{
					Rules: []policyv1alpha1.StatePreservationRule{
						{AliasLabelName: "key-a", JSONPath: "{ .replicas }"},
						{AliasLabelName: "key-b", JSONPath: "{ .health }"},
					},
				},
				rawStatus: []byte(`{"replicas": 2, "health": true}`),
			},
			wantErr: assert.NoError,
			want:    map[string]string{"key-a": "2", "key-b": "true"},
		},
		{
			name: "preserve exact integer checkpoint and offset",
			args: args{
				statePreservation: &policyv1alpha1.StatePreservation{
					Rules: []policyv1alpha1.StatePreservationRule{
						{AliasLabelName: "checkpoint-id", JSONPath: "{ .checkpoint }"},
						{AliasLabelName: "offset", JSONPath: "{ .offset }"},
					},
				},
				rawStatus: []byte(`{"checkpoint": 9007199254740993, "offset": 1000000}`),
			},
			wantErr: assert.NoError,
			want:    map[string]string{"checkpoint-id": "9007199254740993", "offset": "1000000"},
		},
		{
			name: "one statePreservation rule exist not found field",
			args: args{
				statePreservation: &policyv1alpha1.StatePreservation{
					Rules: []policyv1alpha1.StatePreservationRule{
						{AliasLabelName: "key-a", JSONPath: "{ .replicas }"},
						{AliasLabelName: "key-b", JSONPath: "{ .notfound }"},
					},
				},
				rawStatus: []byte(`{"replicas": 2, "health": true}`),
			},
			wantErr: assert.Error,
			want:    nil,
		},
		{
			name: "one statePreservation rule has invalid jsonPath",
			args: args{
				statePreservation: &policyv1alpha1.StatePreservation{
					Rules: []policyv1alpha1.StatePreservationRule{
						{AliasLabelName: "key-a", JSONPath: "{ .replicas }"},
						{AliasLabelName: "key-b", JSONPath: "{ %health }"},
					},
				},
				rawStatus: []byte(`{"replicas": 2, "health": true}`),
			},
			wantErr: assert.Error,
			want:    nil,
		},
		{
			name: "empty rules do not require status decoding",
			args: args{
				statePreservation: &policyv1alpha1.StatePreservation{},
				rawStatus:         []byte(`invalid JSON`),
			},
			wantErr: assert.NoError,
			want:    map[string]string{},
		},
		{
			name: "invalid status is rejected",
			args: args{
				statePreservation: &policyv1alpha1.StatePreservation{
					Rules: []policyv1alpha1.StatePreservationRule{
						{AliasLabelName: "checkpoint", JSONPath: "{ .checkpoint }"},
					},
				},
				rawStatus: []byte(`invalid JSON`),
			},
			wantErr: assert.Error,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPreservedLabelState(tt.args.statePreservation, tt.args.rawStatus)
			if !tt.wantErr(t, err, fmt.Sprintf("buildPreservedLabelState(%v, %s)", tt.args.statePreservation, tt.args.rawStatus)) {
				return
			}
			assert.Equalf(t, tt.want, got, "buildPreservedLabelState(%v, %s)", tt.args.statePreservation, tt.args.rawStatus)
		})
	}
}

func TestBuildPreservedLabelStateJSONPath(t *testing.T) {
	// This json value describes a DeploymentList object, it contains two deployment elements.
	var deploymentListStrBytes = []byte(`{"apiVersion":"v1","items":[{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"creationTimestamp":"2024-11-27T07:59:13Z","generation":2,"labels":{"app":"nginx","propagationpolicy.karmada.io/permanent-id":"89a95e21-57ec-4f5d-b8c6-15bc196c1449"},"name":"nginx-01","namespace":"default","resourceVersion":"1148","uid":"12cf1e9f-61fd-4e47-a14e-e844165c7f93"},"spec":{"progressDeadlineSeconds":600,"replicas":2,"revisionHistoryLimit":10,"selector":{"matchLabels":{"app":"nginx"}},"strategy":{"rollingUpdate":{"maxSurge":"25%","maxUnavailable":"25%"},"type":"RollingUpdate"},"template":{"metadata":{"creationTimestamp":null,"labels":{"app":"nginx"}},"spec":{"containers":[{"image":"nginx","imagePullPolicy":"Always","name":"nginx","resources":{},"terminationMessagePath":"/dev/termination-log","terminationMessagePolicy":"File"}],"dnsPolicy":"ClusterFirst","restartPolicy":"Always","schedulerName":"default-scheduler","securityContext":{},"terminationGracePeriodSeconds":30}}},"status":{"availableReplicas":2,"observedGeneration":2,"readyReplicas":2,"replicas":2,"updatedReplicas":2}},{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"creationTimestamp":"2024-11-27T07:59:13Z","generation":2,"labels":{"app":"nginx","propagationpolicy.karmada.io/permanent-id":"89a95e21-57ec-4f5d-b8c6-15bc196c1449"},"name":"nginx-02","namespace":"default","resourceVersion":"1149","uid":"12cf1e9f-61fd-4e47-a14e-e844165c7f93"},"spec":{"progressDeadlineSeconds":600,"replicas":2,"revisionHistoryLimit":10,"selector":{"matchLabels":{"app":"nginx"}},"strategy":{"rollingUpdate":{"maxSurge":"25%","maxUnavailable":"25%"},"type":"RollingUpdate"},"template":{"metadata":{"creationTimestamp":null,"labels":{"app":"nginx"}},"spec":{"containers":[{"image":"nginx","imagePullPolicy":"Always","name":"nginx","resources":{},"terminationMessagePath":"/dev/termination-log","terminationMessagePolicy":"File"}],"dnsPolicy":"ClusterFirst","restartPolicy":"Always","schedulerName":"default-scheduler","securityContext":{},"terminationGracePeriodSeconds":30}}},"status":{"availableReplicas":2,"observedGeneration":2,"readyReplicas":2,"replicas":2,"updatedReplicas":2}}],"kind":"List","metadata":{"resourceVersion":""}}`)
	type args struct {
		rawStatus []byte
		jsonPath  string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		// Build the following test cases from the perspective of parsing application state
		{
			name: "target field not found",
			args: args{
				rawStatus: []byte(`{"readyReplicas": 2}`),
				jsonPath:  "{ .replicas }",
			},
			wantErr: assert.Error,
		},
		{
			name: "invalid jsonPath",
			args: args{
				rawStatus: []byte(`{"readyReplicas": 2}`),
				jsonPath:  "{ %replicas }",
			},
			wantErr: assert.Error,
		},
		{
			name: "success to parse",
			args: args{
				rawStatus: []byte(`{"replicas": 2}`),
				jsonPath:  "{ .replicas }",
			},
			wantErr: assert.NoError,
			want:    "2",
		},
		{
			name: "large integer is not rounded",
			args: args{
				rawStatus: []byte(`{"checkpoint": 9007199254740993}`),
				jsonPath:  "{ .checkpoint }",
			},
			wantErr: assert.NoError,
			want:    "9007199254740993",
		},
		{
			name: "maximum int64 is preserved",
			args: args{
				rawStatus: []byte(`{"checkpoint": 9223372036854775807}`),
				jsonPath:  "{ .checkpoint }",
			},
			wantErr: assert.NoError,
			want:    "9223372036854775807",
		},
		{
			name: "fractional value is preserved",
			args: args{
				rawStatus: []byte(`{"progress": 1.25}`),
				jsonPath:  "{ .progress }",
			},
			wantErr: assert.NoError,
			want:    "1.25",
		},
		{
			name: "integer filter distinguishes adjacent large values",
			args: args{
				rawStatus: []byte(`{"items":[{"checkpoint":9007199254740992,"name":"old"},{"checkpoint":9007199254740993,"name":"new"}]}`),
				jsonPath:  `{ .items[?(@.checkpoint==9007199254740993)].name }`,
			},
			wantErr: assert.NoError,
			want:    "new",
		},
		// Build the following test cases in terms of what the function supports (which we don't use now).
		// Please refer to Function Support: https://kubernetes.io/docs/reference/kubectl/jsonpath/
		{
			name: "the current object parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{ @ }",
			},
			wantErr: assert.NoError,
			want:    string(deploymentListStrBytes),
		},
		{
			name: "child operator parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{ ['kind'] }",
			},
			wantErr: assert.NoError,
			want:    "List",
		},
		{
			name: "recursive descent parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{ ..resourceVersion }",
			},
			wantErr: assert.NoError,
			want:    "1148 1149",
		},
		{
			name: "wildcard get all objects parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{ .items[*].metadata.name }",
			},
			wantErr: assert.NoError,
			want:    "nginx-01 nginx-02",
		},
		{
			name: "subscript operator parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{ .items[0].metadata.name }",
			},
			wantErr: assert.NoError,
			want:    "nginx-01",
		},
		{
			name: "filter parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{ .items[?(@.metadata.name==\"nginx-01\")].metadata.name }",
			},
			wantErr: assert.NoError,
			want:    "nginx-01",
		},
		{
			name: "iterate list parse",
			args: args{
				rawStatus: deploymentListStrBytes,
				jsonPath:  "{range .items[*]}[{.metadata.name}, {.metadata.namespace}] {end}",
			},
			wantErr: assert.NoError,
			want:    "[nginx-01, default] [nginx-02, default]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := BuildPreservedLabelState(&policyv1alpha1.StatePreservation{
				Rules: []policyv1alpha1.StatePreservationRule{
					{AliasLabelName: "value", JSONPath: tt.args.jsonPath},
				},
			}, tt.args.rawStatus)
			if !tt.wantErr(t, err, fmt.Sprintf("BuildPreservedLabelState(%s, %v)", tt.args.rawStatus, tt.args.jsonPath)) {
				return
			}
			got := strings.Trim(state["value"], " ")
			assert.Equalf(t, tt.want, got, "BuildPreservedLabelState(%s, %v)", tt.args.rawStatus, tt.args.jsonPath)
		})
	}
}

func TestBuildPreservedLabelStateDecodeAllocations(t *testing.T) {
	allocations := func(ruleCount int) float64 {
		t.Helper()
		rules, status := preservedStateTestInputs(ruleCount)
		var err error
		count := testing.AllocsPerRun(5, func() {
			_, err = BuildPreservedLabelState(rules, status)
		})
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	singleRule := allocations(1)
	multipleRules := allocations(10)
	if multipleRules > 2*singleRule {
		t.Errorf("10 rules used %.0f allocations versus %.0f for one rule; status must not be decoded per rule", multipleRules, singleRule)
	}
}

func BenchmarkBuildPreservedLabelState(b *testing.B) {
	for _, ruleCount := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("rules=%d", ruleCount), func(b *testing.B) {
			rules, status := preservedStateTestInputs(ruleCount)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := BuildPreservedLabelState(rules, status); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func preservedStateTestInputs(ruleCount int) (*policyv1alpha1.StatePreservation, []byte) {
	rules := &policyv1alpha1.StatePreservation{}
	for i := range ruleCount {
		rules.Rules = append(rules.Rules, policyv1alpha1.StatePreservationRule{
			AliasLabelName: fmt.Sprintf("checkpoint-%d", i),
			JSONPath:       "{ .checkpoint }",
		})
	}
	history := strings.Repeat(`{"checkpoint":1},`, 511) + `{"checkpoint":1}`
	status := []byte(`{"checkpoint":9007199254740993,"history":[` + history + `]}`)
	return rules, status
}
