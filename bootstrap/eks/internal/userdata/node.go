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
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	eksbootstrapv1 "sigs.k8s.io/cluster-api-provider-aws/v2/bootstrap/eks/api/v1beta2"
	"sigs.k8s.io/cluster-api-provider-aws/v2/exp/api/v1beta2"
)

const (
	boundary = "//"

	nodeUserData = `
--{{.Boundary}}
Content-Type: text/cloud-config
MIME-Version: 1.0
Content-Transfer-Encoding: 7bit
Content-Disposition: attachment; filename="cloud-config.yaml"

#cloud-config
{{template "files" .Files}}
runcmd:
{{- template "ntp" .NTP }}
{{- template "users" .Users }}
{{- template "disk_setup" .DiskSetup}}
{{- template "fs_setup" .DiskSetup}}
{{- template "mounts" .Mounts}}
--{{.Boundary}}--`

	// Shell script part template for nodeadm.
	shellScriptPartTemplate = `
--{{.Boundary}}
Content-Type: text/x-shellscript; charset="us-ascii"
MIME-Version: 1.0
Content-Transfer-Encoding: 7bit
Content-Disposition: attachment; filename="commands.sh"

#!/bin/bash
set -o errexit
set -o pipefail
set -o nounset
{{- if or .PreBootstrapCommands .PostBootstrapCommands }}

{{- range .PreBootstrapCommands}}
{{.}}
{{- end}}
{{- range .PostBootstrapCommands}}
{{.}}
{{- end}}
{{- end}}
--{{ .Boundary }}--`

	// Node config part template for nodeadm.
	nodeConfigPartTemplate = `
--{{.Boundary}}
Content-Type: application/node.eks.aws

---
apiVersion: node.eks.aws/v1alpha1
kind: NodeConfig
spec:
  cluster:
    name: {{.ClusterName}}
    apiServerEndpoint: {{.APIServerEndpoint}}
    certificateAuthority: {{.CACert}}
    cidr: {{if .ServiceCIDR}}{{.ServiceCIDR}}{{else}}172.20.0.0/16{{end}}
  kubelet:
    config:
      maxPods: {{.MaxPods}}
      {{- with .DNSClusterIP }}
      clusterDNS:
      - {{.}}
      {{- end }}
    flags:
    - "--node-labels={{.NodeLabels}}"
    {{- range $key, $value := .KubeletExtraArgs }}
    {{- if ne $key "node-labels" }}
    - "--{{$key}}={{$value}}"
    {{- end }}
    {{- end }}

--{{.Boundary}}--`

	nodeLabelImage        = "eks.amazonaws.com/nodegroup-image=%s"
	nodeLabelNodeGroup    = "eks.amazonaws.com/nodegroup=%s"
	nodeLabelCapacityType = "eks.amazonaws.com/capacityType=%s"
)

// NodeInput contains all the information required to generate user data for a node.
type NodeInput struct {
	ClusterName           string
	KubeletExtraArgs      map[string]string
	ContainerRuntime      *string
	DNSClusterIP          *string
	DockerConfigJSON      *string
	APIRetryAttempts      *int
	PauseContainerAccount *string
	PauseContainerVersion *string
	UseMaxPods            *bool
	// NOTE: currently the IPFamily/ServiceIPV6Cidr isn't exposed to the user.
	// TODO (richardcase): remove the above comment when IPV6 / dual stack is implemented.
	IPFamily                 *string
	ServiceIPV6Cidr          *string
	PreBootstrapCommands     []string
	PostBootstrapCommands    []string
	BootstrapCommandOverride *string
	Files                    []eksbootstrapv1.File
	DiskSetup                *eksbootstrapv1.DiskSetup
	Mounts                   []eksbootstrapv1.MountPoints
	Users                    []eksbootstrapv1.User
	NTP                      *eksbootstrapv1.NTP

	AMIImageID        string
	APIServerEndpoint string
	Boundary          string
	CACert            string
	CapacityType      *v1beta2.ManagedMachinePoolCapacityType
	ServiceCIDR       string // Service CIDR range for the cluster
	ClusterDNS        string
	MaxPods           *int32
	NodeGroupName     string
	NodeLabels        string // Not exposed in CRD, computed from user input
}

// PauseContainerInfo holds pause container information for templates.
type PauseContainerInfo struct {
	AccountNumber *string
	Version       *string
}

// NewNode returns the user data string to be used on a node instance.
func NewNode(input *NodeInput) ([]byte, error) {
	if err := validateNodeInput(input); err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	// Write MIME header
	if _, err := buf.WriteString(fmt.Sprintf("MIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=%q\n\n", input.Boundary)); err != nil {
		return nil, fmt.Errorf("failed to write MIME header: %v", err)
	}

	// Write shell script part if needed
	if len(input.PreBootstrapCommands) > 0 || len(input.PostBootstrapCommands) > 0 {
		shellScriptTemplate := template.Must(template.New("shell").Parse(shellScriptPartTemplate))
		if err := shellScriptTemplate.Execute(&buf, input); err != nil {
			return nil, fmt.Errorf("failed to execute shell script template: %v", err)
		}
		if _, err := buf.WriteString("\n"); err != nil {
			return nil, fmt.Errorf("failed to write newline: %v", err)
		}
	}

	// Write node config part
	nodeConfigTemplate := template.Must(template.New("node").Parse(nodeConfigPartTemplate))
	if err := nodeConfigTemplate.Execute(&buf, input); err != nil {
		return nil, fmt.Errorf("failed to execute node config template: %v", err)
	}

	// Write cloud-config part
	tm := template.New("Node").Funcs(defaultTemplateFuncMap)
	// if any of the input fields are set, we need to write the cloud-config part
	if input.NTP != nil || input.DiskSetup != nil || input.Mounts != nil || input.Users != nil {
		if _, err := tm.Parse(filesTemplate); err != nil {
			return nil, fmt.Errorf("failed to parse args template: %w", err)
		}
		if _, err := tm.Parse(ntpTemplate); err != nil {
			return nil, fmt.Errorf("failed to parse ntp template: %w", err)
		}

		if _, err := tm.Parse(usersTemplate); err != nil {
			return nil, fmt.Errorf("failed to parse users template: %w", err)
		}

		if _, err := tm.Parse(diskSetupTemplate); err != nil {
			return nil, fmt.Errorf("failed to parse disk setup template: %w", err)
		}

		if _, err := tm.Parse(fsSetupTemplate); err != nil {
			return nil, fmt.Errorf("failed to parse fs setup template: %w", err)
		}

		if _, err := tm.Parse(mountsTemplate); err != nil {
			return nil, fmt.Errorf("failed to parse mounts template: %w", err)
		}

		t, err := tm.Parse(nodeUserData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Node template: %w", err)
		}

		if err := t.Execute(&buf, input); err != nil {
			return nil, fmt.Errorf("failed to execute node user data template: %w", err)
		}
	}
	return buf.Bytes(), nil

}

// getNodeLabels returns the string representation of node-labels flags for nodeadm.
func (ni *NodeInput) getNodeLabels() string {
	var nodeLabels string
	if ni.KubeletExtraArgs != nil {
		if nodeLabelsValue, ok := ni.KubeletExtraArgs["node-labels"]; ok {
			nodeLabels = nodeLabelsValue
		}
	}
	extraLabels := make([]string, 0, 3)
	if ni.AMIImageID != "" && !strings.Contains(nodeLabels, nodeLabelImage) {
		extraLabels = append(extraLabels, fmt.Sprintf(nodeLabelImage, ni.AMIImageID))
	}
	if ni.NodeGroupName != "" && !strings.Contains(nodeLabels, nodeLabelImage) {
		extraLabels = append(extraLabels, fmt.Sprintf(nodeLabelNodeGroup, ni.NodeGroupName))
	}
	extraLabels = append(extraLabels, fmt.Sprintf(nodeLabelCapacityType, ni.getCapacityTypeString()))
	if nodeLabels != "" {
		return fmt.Sprintf("%s,%s", nodeLabels, strings.Join(extraLabels, ","))
	}
	return strings.Join(extraLabels, ",")
}

// getCapacityTypeString returns the string representation of the capacity type.
func (ni *NodeInput) getCapacityTypeString() string {
	if ni.CapacityType == nil {
		return "ON_DEMAND"
	}
	switch *ni.CapacityType {
	case v1beta2.ManagedMachinePoolCapacityTypeSpot:
		return "SPOT"
	case v1beta2.ManagedMachinePoolCapacityTypeOnDemand:
		return "ON_DEMAND"
	default:
		return strings.ToUpper(string(*ni.CapacityType))
	}
}

// validateNodeInput validates the input for nodeadm user data generation.
func validateNodeInput(input *NodeInput) error {
	if input.APIServerEndpoint == "" {
		return fmt.Errorf("API server endpoint is required for nodeadm")
	}
	if input.CACert == "" {
		return fmt.Errorf("CA certificate is required for nodeadm")
	}
	if input.ClusterName == "" {
		return fmt.Errorf("cluster name is required for nodeadm")
	}
	if input.NodeGroupName == "" {
		return fmt.Errorf("node group name is required for nodeadm")
	}

	if input.MaxPods == nil {
		if input.UseMaxPods != nil && *input.UseMaxPods {
			input.MaxPods = ptr.To[int32](110)
		} else {
			input.MaxPods = ptr.To[int32](58)
		}
	}
	if input.DNSClusterIP != nil {
		input.ClusterDNS = *input.DNSClusterIP
	}

	if input.Boundary == "" {
		input.Boundary = boundary
	}
	input.NodeLabels = input.getNodeLabels()

	klog.V(2).Infof("Nodeadm Userdata Generation - maxPods: %d, node-labels: %s",
		*input.MaxPods, input.NodeLabels)

	return nil
}
