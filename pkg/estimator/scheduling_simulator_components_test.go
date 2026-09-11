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

package estimator

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karmada-io/karmada/pkg/estimator/pb"
	"github.com/karmada-io/karmada/pkg/util"
	schedulerframework "github.com/karmada-io/karmada/pkg/util/lifted/scheduler/framework"
)

func TestMatchNode(t *testing.T) {
	tests := []struct {
		name                string
		replicaRequirements *pb.ReplicaRequirements
		node                *schedulerframework.NodeInfo
		expected            bool
	}{
		{
			name: "no enough information to perform the match operation - should match",
			replicaRequirements: (&pb.ReplicaRequirements{
				NodeClaim: (&pb.NodeClaim{}).MustSetNodeAffinity(&corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "zone",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"us-west"},
								},
							},
						},
					},
				}),
			}).MustSetResourceRequest(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}),
			node: func() *schedulerframework.NodeInfo {
				return schedulerframework.NewNodeInfo()
			}(),
			expected: true,
		},
		{
			name: "no constraints - should match",
			replicaRequirements: (&pb.ReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}),
			node: func() *schedulerframework.NodeInfo {
				nodeInfo := schedulerframework.NewNodeInfo()
				nodeInfo.SetNode(makeNode("node1", map[string]string{}, corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"),
				}))
				return nodeInfo
			}(),
			expected: true,
		},
		{
			name: "node affinity matches",
			replicaRequirements: (&pb.ReplicaRequirements{
				NodeClaim: (&pb.NodeClaim{}).MustSetNodeAffinity(&corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "zone",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"us-west"},
								},
							},
						},
					},
				}),
			}).MustSetResourceRequest(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}),
			node: func() *schedulerframework.NodeInfo {
				nodeInfo := schedulerframework.NewNodeInfo()
				nodeInfo.SetNode(makeNode("node1", map[string]string{"zone": "us-west"}, corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"),
				}))
				return nodeInfo
			}(),
			expected: true,
		},
		{
			name: "node affinity does not match",
			replicaRequirements: (&pb.ReplicaRequirements{
				NodeClaim: (&pb.NodeClaim{}).MustSetNodeAffinity(&corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "zone",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"us-west"},
								},
							},
						},
					},
				}),
			}).MustSetResourceRequest(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}),
			node: func() *schedulerframework.NodeInfo {
				nodeInfo := schedulerframework.NewNodeInfo()
				nodeInfo.SetNode(makeNode("node1", map[string]string{"zone": "us-east"}, corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"),
				}))
				return nodeInfo
			}(),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			affinity, tolerations, err := GetAffinityAndTolerations(tt.replicaRequirements.NodeClaim)
			if err != nil {
				t.Fatalf("GetAffinityAndTolerations() error: %v", err)
			}
			result := MatchNode(tt.node, affinity, tolerations)
			if result != tt.expected {
				t.Errorf("MatchNode() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSchedulingSimulatorZeroReplicaAllocations(t *testing.T) {
	components := []*pb.Component{{
		Name: "scaled-to-zero",
		ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		}),
	}}
	node := createNodeInfo("node", corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
		corev1.ResourcePods:   resource.MustParse("10"),
	})
	original := node.Allocatable.Clone()
	simulator := NewSchedulingSimulator([]*schedulerframework.NodeInfo{node})
	allocations := func(upperBound int32) float64 {
		t.Helper()
		var count int32
		var err error
		allocs := testing.AllocsPerRun(5, func() {
			count, err = simulator.SimulateScheduling(components, upperBound)
		})
		if err != nil || count != upperBound {
			t.Fatalf("SimulateScheduling() = (%d, %v), want (%d, nil)", count, err, upperBound)
		}
		return allocs
	}
	small, large := allocations(1), allocations(1000)
	if large > small+2 {
		t.Fatalf("zero-replica work used %.0f allocations for 1000 sets versus %.0f for one set", large, small)
	}
	if count, err := simulator.SimulateScheduling(components, math.MaxInt32); err != nil || count != math.MaxInt32 {
		t.Errorf("SimulateScheduling() = (%d, %v), want (%d, nil)", count, err, math.MaxInt32)
	}
	if !reflect.DeepEqual(node.Allocatable, original) {
		t.Error("zero-replica work consumed node resources")
	}
}

func TestSchedulingSimulatorZeroReplicaInputs(t *testing.T) {
	invalid := &pb.Component{
		Name: "invalid",
		ReplicaRequirements: &pb.ComponentReplicaRequirements{
			ResourceRequestBytes: map[string][]byte{"cpu": {0xff}},
		},
	}
	for _, tt := range []struct {
		name       string
		components []*pb.Component
		upperBound int32
		want       int32
		wantErr    bool
	}{
		{name: "no components", upperBound: 10, want: 10},
		{name: "nil component", components: []*pb.Component{nil}, upperBound: 10, want: 10},
		{name: "zero replicas", components: []*pb.Component{{Name: "zero"}}, upperBound: 10, want: 10},
		{name: "zero bound", components: []*pb.Component{invalid}},
		{name: "negative bound", upperBound: -1},
		{name: "invalid zero-replica requirements still fail", components: []*pb.Component{invalid}, upperBound: 10, wantErr: true},
		{name: "positive component still needs capacity", components: []*pb.Component{nil, {Name: "zero"}, {Name: "positive", Replicas: 1}}, upperBound: 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewSchedulingSimulator(nil).SimulateScheduling(tt.components, tt.upperBound)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("SimulateScheduling() = (%d, %v), want count %d, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestSchedulingSimulatorRejectsNegativeReplicas(t *testing.T) {
	requirements := (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	})
	negative := &pb.Component{Name: "negative", Replicas: -1, ReplicaRequirements: requirements}
	positive := &pb.Component{Name: "positive", Replicas: 1, ReplicaRequirements: requirements}
	for _, tt := range []struct {
		name       string
		components []*pb.Component
		upperBound int32
		wantErr    bool
	}{
		{name: "negative first", components: []*pb.Component{negative}, upperBound: 1, wantErr: true},
		{name: "negative after valid work", components: []*pb.Component{positive, negative}, upperBound: 1, wantErr: true},
		{name: "minimum replicas", components: []*pb.Component{{Name: "minimum", Replicas: math.MinInt32, ReplicaRequirements: requirements}}, upperBound: 1, wantErr: true},
		{name: "zero bound remains a no-op", components: []*pb.Component{negative}},
		{name: "negative bound remains a no-op", components: []*pb.Component{negative}, upperBound: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := createNodeInfo("node", corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			})
			original := node.Allocatable.Clone()
			got, err := NewSchedulingSimulator([]*schedulerframework.NodeInfo{node}).SimulateScheduling(tt.components, tt.upperBound)
			if (err != nil) != tt.wantErr || got != 0 {
				t.Errorf("SimulateScheduling() = (%d, %v), want (0, error=%v)", got, err, tt.wantErr)
			}
			if !reflect.DeepEqual(node.Allocatable, original) {
				t.Errorf("invalid replica count changed capacity: got %+v, want %+v", node.Allocatable, original)
			}
		})
	}
}

func BenchmarkSimulateSchedulingZeroReplicas(b *testing.B) {
	components := []*pb.Component{{
		Name: "scaled-to-zero",
		ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1"),
		}),
	}}
	simulator := NewSchedulingSimulator(nil)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := simulator.SimulateScheduling(components, 1000); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSchedulingSimulator_SimulateSchedulingFF(t *testing.T) {
	tests := []struct {
		name         string
		nodes        []*schedulerframework.NodeInfo
		components   []*pb.Component
		upperBound   int32
		expectedSets int32
	}{
		{
			name: "single component fits perfectly",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("node1", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "web",
					Replicas: 2,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					}),
				},
			},
			upperBound:   10,
			expectedSets: 2, // 4 CPU / 1 CPU per replica = 4 replicas, 4 / 2 replicas per set = 2 sets
		},
		{
			name: "multiple components with resource constraints",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("node1", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "web",
					Replicas: 2,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					}),
				},
				{
					Name:     "db",
					Replicas: 1,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					}),
				},
			},
			upperBound:   10,
			expectedSets: 2, // Each set needs 4 CPU (2*1 + 1*2) and 8Gi memory (2*2 + 1*4), so 2 sets fit
		},
		{
			name: "no resources available",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("node1", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("0"),
					corev1.ResourceMemory: resource.MustParse("0"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "web",
					Replicas: 1,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					}),
				},
			},
			upperBound:   10,
			expectedSets: 0,
		},
		{
			name: "upper bound limits scheduling",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("node1", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("100"),
					corev1.ResourcePods: resource.MustParse("10"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "web",
					Replicas: 1,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					}),
				},
			},
			upperBound:   3,
			expectedSets: 3, // Limited by upper bound, not resources
		},
		{
			name: "multiple nodes distribution",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("node1", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("2"),
					corev1.ResourcePods: resource.MustParse("10"),
				}),
				createNodeInfo("node2", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("2"),
					corev1.ResourcePods: resource.MustParse("10"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "web",
					Replicas: 3,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					}),
				},
			},
			upperBound:   10,
			expectedSets: 1, // Total 4 CPU, need 3 CPU per set, so 1 set fits
		},
		{
			name: "nodes with different capacities",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("small-node", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}),
				createNodeInfo("medium-node", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
					corev1.ResourcePods:   resource.MustParse("20"),
				}),
				createNodeInfo("large-node", corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
					corev1.ResourcePods:   resource.MustParse("30"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "frontend",
					Replicas: 2,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					}),
				},
				{
					Name:     "backend",
					Replicas: 1,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					}),
				},
			},
			upperBound:   10,
			expectedSets: 4,
		},
		{
			name: "many small nodes",
			nodes: []*schedulerframework.NodeInfo{
				createNodeInfo("node1", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("1"),
					corev1.ResourcePods: resource.MustParse("5"),
				}),
				createNodeInfo("node2", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("1"),
					corev1.ResourcePods: resource.MustParse("5"),
				}),
				createNodeInfo("node3", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("1"),
					corev1.ResourcePods: resource.MustParse("5"),
				}),
				createNodeInfo("node4", corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("1"),
					corev1.ResourcePods: resource.MustParse("5"),
				}),
			},
			components: []*pb.Component{
				{
					Name:     "microservice",
					Replicas: 1,
					ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					}),
				},
			},
			upperBound:   10,
			expectedSets: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			simulator := NewSchedulingSimulator(tt.nodes)
			result, err := simulator.SimulateScheduling(tt.components, tt.upperBound)
			if err != nil {
				t.Fatalf("SimulateScheduling() error: %v", err)
			}
			if result != tt.expectedSets {
				t.Errorf("SimulateScheduling() = %d, expected %d", result, tt.expectedSets)
			}
		})
	}
}

// BenchmarkSimulateScheduling_AssumedWorkloadDeduction benchmarks the deduction loop
// used in the noderesource estimator plugin: for each assumed workload, call
// SimulateScheduling with upperBound=1 to consume its node resources.
//
// Scenario assumptions:
//   - Scheduler throughput ~100 RB/min, TTL 5 min → up to 500 assumed workloads in cache
//   - Each assumed workload has 2 components (typical multi-template app: web + db)
//   - Node clusters of 100 / 500 / 1000 nodes
func BenchmarkSimulateScheduling_AssumedWorkloadDeduction(b *testing.B) {
	nodeSizes := []int{100, 500, 1000}
	const assumedCount = 500

	// Build a typical 2-component assumed workload (web + db pattern)
	makeAssumedComponents := func() []*pb.Component {
		return []*pb.Component{
			{
				Name:     "web",
				Replicas: 2,
				ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				}),
			},
			{
				Name:     "db",
				Replicas: 1,
				ReplicaRequirements: (&pb.ComponentReplicaRequirements{}).MustSetResourceRequest(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				}),
			},
		}
	}

	// Build 500 assumed workloads
	assumedWorkloads := make([][]*pb.Component, assumedCount)
	for i := range assumedWorkloads {
		assumedWorkloads[i] = makeAssumedComponents()
	}

	// makeNodes returns fresh node clones (nodes are mutated during simulation)
	makeNodes := func(n int) []*schedulerframework.NodeInfo {
		nodes := make([]*schedulerframework.NodeInfo, n)
		for i := range nodes {
			allocatable := corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("16"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			}
			nodes[i] = createNodeInfo(fmt.Sprintf("node-%d", i), allocatable)
		}
		return nodes
	}

	for _, nodeCount := range nodeSizes {
		b.Run(fmt.Sprintf("nodes=%d/assumedWorkloads=%d", nodeCount, assumedCount), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				// Each benchmark iteration simulates one EstimateComponents call:
				// clone fresh nodes (mimics getNodesAvailableResources) then deduct all assumed workloads
				nodes := makeNodes(nodeCount)
				simulator := NewSchedulingSimulator(nodes)
				for _, components := range assumedWorkloads {
					_, _ = simulator.SimulateScheduling(components, 1)
				}
			}
		})
	}
}

// Helper functions

func createNodeInfo(name string, allocatable corev1.ResourceList) *schedulerframework.NodeInfo {
	nodeInfo := schedulerframework.NewNodeInfo()
	nodeInfo.SetNode(makeNode(name, map[string]string{}, allocatable))
	// Set the Allocatable field using util.NewResource
	nodeInfo.Allocatable = util.NewResource(allocatable)
	return nodeInfo
}

func makeNode(name string, labels map[string]string, allocatable corev1.ResourceList) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: corev1.NodeStatus{
			Allocatable: allocatable,
			Capacity:    allocatable,
		},
	}
}
