package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ThermalPolicySpec defines the temperature thresholds used to taint hot nodes.
// +kubebuilder:validation:XValidation:rule="self.coolCelsius < self.hotCelsius",message="coolCelsius must be lower than hotCelsius"
type ThermalPolicySpec struct {
	// Nodes at or above this temperature get the hot taint.
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:validation:Maximum=90
	// +kubebuilder:default=75
	HotCelsius int32 `json:"hotCelsius"`

	// Nodes at or below this temperature lose the hot taint.
	// +kubebuilder:validation:Minimum=20
	// +kubebuilder:validation:Maximum=89
	// +kubebuilder:default=68
	CoolCelsius int32 `json:"coolCelsius"`
}

// ThermalPolicyStatus defines the observed state of ThermalPolicy.
type ThermalPolicyStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=tp
// +kubebuilder:printcolumn:name="Hot",type=integer,JSONPath=`.spec.hotCelsius`
// +kubebuilder:printcolumn:name="Cool",type=integer,JSONPath=`.spec.coolCelsius`

// ThermalPolicy is the Schema for the thermalpolicies API.
type ThermalPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ThermalPolicySpec   `json:"spec,omitempty"`
	Status ThermalPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ThermalPolicyList contains a list of ThermalPolicy.
type ThermalPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ThermalPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &ThermalPolicy{}, &ThermalPolicyList{})
		return nil
	})
}
