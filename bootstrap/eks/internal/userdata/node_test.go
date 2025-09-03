/*
Copyright 2020 The Kubernetes Authors.

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

package userdata

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"k8s.io/utils/ptr"
)

func TestNewNode(t *testing.T) {
	format.TruncatedDiff = false
	g := NewWithT(t)

	type args struct {
		input *NodeInput
	}

	tests := []struct {
		name         string
		args         args
		expectErr    bool
		verifyOutput func(output string) bool
	}{
		{
			name: "basic nodeadm userdata",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://example.com",
					CACert:            "test-ca-cert",
					NodeGroupName:     "test-nodegroup",
				},
			},
			expectErr: false,
			verifyOutput: func(output string) bool {
				return strings.Contains(output, "MIME-Version: 1.0") &&
					strings.Contains(output, "name: test-cluster") &&
					strings.Contains(output, "apiServerEndpoint: https://example.com") &&
					strings.Contains(output, "certificateAuthority: test-ca-cert") &&
					strings.Contains(output, "apiVersion: node.eks.aws/v1alpha1")
			},
		},
		{
			name: "with kubelet extra args",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://example.com",
					CACert:            "test-ca-cert",
					NodeGroupName:     "test-nodegroup",
					KubeletExtraArgs: map[string]string{
						"node-labels":          "node-role.undistro.io/infra=true",
						"register-with-taints": "dedicated=infra:NoSchedule",
					},
				},
			},
			expectErr: false,
			verifyOutput: func(output string) bool {
				fmt.Println(output)
				return strings.Contains(output, "node-role.undistro.io/infra=true") &&
					strings.Contains(output, "register-with-taints") &&
					strings.Contains(output, "apiVersion: node.eks.aws/v1alpha1")
			},
		},
		{
			name: "with pre and post bootstrap commands",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://example.com",
					CACert:            "test-ca-cert",
					NodeGroupName:     "test-nodegroup",
					PreBootstrapCommands: []string{
						"echo 'pre-bootstrap'",
						"yum install -y htop",
					},
					PostBootstrapCommands: []string{
						"echo 'post-bootstrap'",
					},
				},
			},
			expectErr: false,
			verifyOutput: func(output string) bool {
				return strings.Contains(output, "echo 'pre-bootstrap'") &&
					strings.Contains(output, "echo 'post-bootstrap'") &&
					strings.Contains(output, "yum install -y htop") &&
					strings.Contains(output, "#!/bin/bash") &&
					strings.Contains(output, "apiVersion: node.eks.aws/v1alpha1")
			},
		},
		{
			name: "with custom DNS and AMI",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://test-endpoint.eks.amazonaws.com",
					CACert:            "test-cert",
					NodeGroupName:     "test-nodegroup",
					UseMaxPods:        ptr.To[bool](true),
					DNSClusterIP:      ptr.To[string]("10.100.0.10"),
					AMIImageID:        "ami-123456",
					ServiceCIDR:       "192.168.0.0/16",
				},
			},
			expectErr: false,
			verifyOutput: func(output string) bool {
				return strings.Contains(output, "cidr: 192.168.0.0/16") &&
					strings.Contains(output, "maxPods: 110") &&
					strings.Contains(output, "nodegroup-image=ami-123456") &&
					strings.Contains(output, "clusterDNS:\n      - 10.100.0.10")
			},
		},
		{
			name: "with capacity type and custom labels",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://test-endpoint.eks.amazonaws.com",
					CACert:            "test-cert",
					NodeGroupName:     "test-nodegroup",
					KubeletExtraArgs: map[string]string{
						"node-labels": "app=my-app,environment=production",
					},
				},
			},
			expectErr: false,
			verifyOutput: func(output string) bool {
				fmt.Println(output)
				return strings.Contains(output, `"--node-labels=app=my-app,environment=production"`)
			},
		},
		{
			name: "missing required fields",
			args: args{
				input: &NodeInput{
					ClusterName: "test-cluster",
					// Missing APIServerEndpoint, CACert, NodeGroupName
				},
			},
			expectErr: true,
		},
		{
			name: "missing API server endpoint",
			args: args{
				input: &NodeInput{
					ClusterName:   "test-cluster",
					CACert:        "test-ca-cert",
					NodeGroupName: "test-nodegroup",
					// Missing APIServerEndpoint
				},
			},
			expectErr: true,
		},
		{
			name: "missing CA certificate",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://example.com",
					NodeGroupName:     "test-nodegroup",
					// Missing CACert
				},
			},
			expectErr: true,
		},
		{
			name: "missing node group name",
			args: args{
				input: &NodeInput{
					ClusterName:       "test-cluster",
					APIServerEndpoint: "https://example.com",
					CACert:            "test-ca-cert",
					// Missing NodeGroupName
				},
			},
			expectErr: true,
		},
	}

	for _, testcase := range tests {
		t.Run(testcase.name, func(t *testing.T) {
			bytes, err := NewNode(testcase.args.input)
			if testcase.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			if testcase.verifyOutput != nil {
				g.Expect(testcase.verifyOutput(string(bytes))).To(BeTrue(), "Output verification failed for: %s", testcase.name)
			}
		})
	}
}
