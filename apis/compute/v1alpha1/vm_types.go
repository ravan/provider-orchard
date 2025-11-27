/*
Copyright 2025 The Crossplane Authors.

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

package v1alpha1

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
)

// VMStartupScript represents a startup script for a VM.
type VMStartupScript struct {
	// ScriptContent is the shell script to run after VM boots
	ScriptContent string `json:"scriptContent,omitempty"`
	// Env is a map of environment variables for the script
	Env map[string]string `json:"env,omitempty"`
}

// VMHostDir represents a host directory to mount to a VM.
type VMHostDir struct {
	// Name of the mount
	Name string `json:"name,omitempty"`
	// Path on the host
	Path string `json:"path,omitempty"`
	// Ro indicates read-only mount
	Ro *bool `json:"ro,omitempty"`
}

// VMParameters are the configurable fields of a VM.
type VMParameters struct {
	// Image is the VM image (e.g., ghcr.io/cirruslabs/macos-sonoma-vanilla:latest)
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// CPU is the number of CPUs assigned to this VM
	// +kubebuilder:validation:Minimum=1
	// +optional
	CPU *int32 `json:"cpu,omitempty"`

	// Memory is the amount of RAM in megabytes assigned to this VM
	// +kubebuilder:validation:Minimum=512
	// +optional
	Memory *int32 `json:"memory,omitempty"`

	// DiskSize is the disk size for this VM in gigabytes
	// +optional
	DiskSize *int32 `json:"diskSize,omitempty"`

	// StartupScript is the startup script to run after the VM boots
	// +optional
	StartupScript *VMStartupScript `json:"startupScript,omitempty"`

	// Username is the SSH username to use when connecting to a VM
	// +optional
	Username *string `json:"username,omitempty"`

	// Password is the SSH password to use when connecting to a VM
	// +optional
	Password *string `json:"password,omitempty"`

	// Headless indicates whether to run without graphics
	// +optional
	Headless *bool `json:"headless,omitempty"`

	// NetBridged specifies whether to use bridged network mode
	// +optional
	NetBridged *string `json:"netBridged,omitempty"`

	// NetSoftnet indicates whether to use Softnet network isolation
	// +optional
	NetSoftnet *bool `json:"netSoftnet,omitempty"`

	// NetSoftnetAllow is a list of CIDRs to allow when using Softnet isolation
	// +optional
	NetSoftnetAllow []string `json:"netSoftnetAllow,omitempty"`

	// NetSoftnetBlock is a list of CIDRs to block when using Softnet isolation
	// +optional
	NetSoftnetBlock []string `json:"netSoftnetBlock,omitempty"`

	// HostDirs are directories on the Orchard Worker host to mount to a VM
	// +optional
	HostDirs []VMHostDir `json:"hostDirs,omitempty"`

	// Labels are labels required by this VM on the worker
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Resources are resources required by this VM on the worker
	// +optional
	Resources map[string]int `json:"resources,omitempty"`

	// Nested enables nested virtualization
	// +optional
	Nested *bool `json:"nested,omitempty"`

	// Suspendable allows the VM to be suspended instead of stopped
	// +optional
	Suspendable *bool `json:"suspendable,omitempty"`

	// ImagePullPolicy is the VM image pull policy (Always, IfNotPresent)
	// +kubebuilder:validation:Enum=Always;IfNotPresent
	// +optional
	ImagePullPolicy *string `json:"imagePullPolicy,omitempty"`

	// RestartPolicy is the VM restart policy (Never, OnFailure)
	// +kubebuilder:validation:Enum=Never;OnFailure
	// +optional
	RestartPolicy *string `json:"restartPolicy,omitempty"`
}

// VMObservation are the observable fields of a VM.
type VMObservation struct {
	// Status is the VM status (pending, running, failed, etc.)
	Status string `json:"status,omitempty"`

	// StatusMessage is the VM status message
	StatusMessage string `json:"statusMessage,omitempty"`

	// Worker is the worker on which the VM was assigned
	Worker string `json:"worker,omitempty"`

	// Generation is incremented by the controller each time a VM's specification changes
	Generation *int32 `json:"generation,omitempty"`

	// ObservedGeneration corresponds to the Generation value on which the worker had acted upon
	ObservedGeneration *int32 `json:"observedGeneration,omitempty"`
}

// A VMSpec defines the desired state of a VM.
type VMSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              VMParameters `json:"forProvider"`
}

// A VMStatus represents the observed state of a VM.
type VMStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          VMObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A VM is a managed resource that represents an Orchard virtual machine.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="STATUS",type="string",JSONPath=".status.atProvider.status"
// +kubebuilder:printcolumn:name="WORKER",type="string",JSONPath=".status.atProvider.worker"
// +kubebuilder:printcolumn:name="IMAGE",type="string",JSONPath=".spec.forProvider.image"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,orchard}
// +kubebuilder:rbac:groups=compute.orchard.crossplane.io,resources=vms,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=compute.orchard.crossplane.io,resources=vms/status,verbs=get;update;patch
type VM struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VMSpec   `json:"spec"`
	Status VMStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VMList contains a list of VM
type VMList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VM `json:"items"`
}

// VM type metadata.
var (
	VMKind             = reflect.TypeOf(VM{}).Name()
	VMGroupKind        = schema.GroupKind{Group: Group, Kind: VMKind}.String()
	VMKindAPIVersion   = VMKind + "." + SchemeGroupVersion.String()
	VMGroupVersionKind = SchemeGroupVersion.WithKind(VMKind)
)

func init() {
	SchemeBuilder.Register(&VM{}, &VMList{})
}
