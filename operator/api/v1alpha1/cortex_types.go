/*
Copyright 2026.

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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImageSpec defines the container image configuration.
type ImageSpec struct {
	// Repository is the container image repository.
	// +kubebuilder:default="quay.io/cortexproject/cortex"
	// +optional
	Repository string `json:"repository,omitempty"`

	// Tag is the container image tag.
	Tag string `json:"tag"`

	// PullPolicy defines the image pull policy.
	// +kubebuilder:default="IfNotPresent"
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`

	// PullSecrets is a list of image pull secrets.
	// +optional
	PullSecrets []corev1.LocalObjectReference `json:"pullSecrets,omitempty"`
}

// StorageBackend defines the backend storage type.
// +kubebuilder:validation:Enum=s3;gcs;azure;swift
type StorageBackend string

const (
	StorageBackendS3    StorageBackend = "s3"
	StorageBackendGCS   StorageBackend = "gcs"
	StorageBackendAzure StorageBackend = "azure"
	StorageBackendSwift StorageBackend = "swift"
)

// StorageSpec defines the object storage configuration.
type StorageSpec struct {
	// Backend is the storage backend type.
	Backend StorageBackend `json:"backend"`

	// S3 contains S3 backend configuration.
	// +optional
	S3 *S3StorageSpec `json:"s3,omitempty"`

	// GCS contains GCS backend configuration.
	// +optional
	GCS *GCSStorageSpec `json:"gcs,omitempty"`

	// Azure contains Azure backend configuration.
	// +optional
	Azure *AzureStorageSpec `json:"azure,omitempty"`

	// Swift contains Swift backend configuration.
	// +optional
	Swift *SwiftStorageSpec `json:"swift,omitempty"`

	// SecretRef is a reference to a Secret containing storage credentials.
	// The secret values are injected as environment variables via -config.expand-env=true.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// S3StorageSpec defines S3-specific storage configuration.
type S3StorageSpec struct {
	// BucketName is the S3 bucket name.
	BucketName string `json:"bucketName"`

	// Endpoint is the S3 endpoint URL.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Region is the S3 region.
	// +optional
	Region string `json:"region,omitempty"`

	// Insecure disables HTTPS for the endpoint.
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}

// GCSStorageSpec defines GCS-specific storage configuration.
type GCSStorageSpec struct {
	// BucketName is the GCS bucket name.
	BucketName string `json:"bucketName"`
}

// AzureStorageSpec defines Azure-specific storage configuration.
type AzureStorageSpec struct {
	// ContainerName is the Azure container name.
	ContainerName string `json:"containerName"`

	// AccountName is the Azure account name.
	// +optional
	AccountName string `json:"accountName,omitempty"`
}

// SwiftStorageSpec defines Swift-specific storage configuration.
type SwiftStorageSpec struct {
	// ContainerName is the Swift container name.
	ContainerName string `json:"containerName"`

	// AuthURL is the Swift authentication URL.
	// +optional
	AuthURL string `json:"authUrl,omitempty"`
}

// RingSpec defines the hash ring configuration.
type RingSpec struct {
	// KVStore is the key-value store for the ring.
	// +kubebuilder:default="memberlist"
	// +optional
	KVStore string `json:"kvStore,omitempty"`

	// ReplicationFactor is the number of replicas for each series.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	ReplicationFactor *int32 `json:"replicationFactor,omitempty"`

	// NumTokens is the number of tokens per ingester in the ring.
	// +kubebuilder:default=512
	// +optional
	NumTokens *int32 `json:"numTokens,omitempty"`
}

// MemberlistSpec defines the memberlist configuration.
type MemberlistSpec struct {
	// BindPort is the port for memberlist gossip.
	// +kubebuilder:default=7946
	// +optional
	BindPort *int32 `json:"bindPort,omitempty"`

	// AbortIfJoinFails controls whether the instance should abort if joining the cluster fails.
	// +kubebuilder:default=false
	// +optional
	AbortIfJoinFails *bool `json:"abortIfJoinFails,omitempty"`
}

// LimitsSpec defines the global limits configuration.
type LimitsSpec struct {
	// IngestionRate is the per-tenant ingestion rate limit in samples per second.
	// +optional
	IngestionRate *float64 `json:"ingestionRate,omitempty"`

	// IngestionBurstSize is the per-tenant allowed burst of samples.
	// +optional
	IngestionBurstSize *int32 `json:"ingestionBurstSize,omitempty"`

	// MaxSeriesPerUser is the maximum number of active series per tenant.
	// +optional
	MaxSeriesPerUser *int64 `json:"maxSeriesPerUser,omitempty"`

	// MaxSeriesPerMetric is the maximum number of active series per metric per tenant.
	// +optional
	MaxSeriesPerMetric *int64 `json:"maxSeriesPerMetric,omitempty"`

	// MaxGlobalSeriesPerUser is the maximum number of active series across all ingesters per tenant.
	// +optional
	MaxGlobalSeriesPerUser *int64 `json:"maxGlobalSeriesPerUser,omitempty"`

	// MaxGlobalSeriesPerMetric is the maximum number of active series per metric across all ingesters per tenant.
	// +optional
	MaxGlobalSeriesPerMetric *int64 `json:"maxGlobalSeriesPerMetric,omitempty"`
}

// RuntimeConfigRef references a ConfigMap containing Cortex runtime config.
type RuntimeConfigRef struct {
	// Name is the name of the ConfigMap containing runtime config.
	Name string `json:"name"`

	// Key is the key in the ConfigMap. Defaults to "runtime.yaml".
	// +kubebuilder:default="runtime.yaml"
	// +optional
	Key string `json:"key,omitempty"`
}

// ExternalConfigSpec references an externally managed Cortex config.
type ExternalConfigSpec struct {
	// ConfigMapRef is a reference to a ConfigMap containing the full Cortex config.
	ConfigMapRef corev1.LocalObjectReference `json:"configMapRef"`

	// Key is the key in the ConfigMap. Defaults to "cortex.yaml".
	// +kubebuilder:default="cortex.yaml"
	// +optional
	Key string `json:"key,omitempty"`
}

// ComponentSpec defines common component configuration.
type ComponentSpec struct {
	// Replicas is the number of replicas.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources defines the compute resources for the component.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector is a map of node labels for pod assignment.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations defines tolerations for the component pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity defines the pod affinity rules.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// ExtraArgs is a list of additional command-line arguments.
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// ExtraEnv is a list of additional environment variables.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`
}

// IngesterComponentSpec extends ComponentSpec with ingester-specific settings.
type IngesterComponentSpec struct {
	ComponentSpec `json:",inline"`

	// DataVolumeClaimSpec defines the PVC template for the ingester WAL.
	// +optional
	DataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec `json:"dataVolumeClaimSpec,omitempty"`

	// TerminationGracePeriodSeconds is the grace period for ingester shutdown.
	// +kubebuilder:default=2400
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
}

// StoreGatewaySpec extends ComponentSpec with store-gateway-specific settings.
type StoreGatewaySpec struct {
	ComponentSpec `json:",inline"`

	// DataVolumeClaimSpec defines the PVC template for the store-gateway cache.
	// +optional
	DataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec `json:"dataVolumeClaimSpec,omitempty"`

	// ShardingEnabled enables store-gateway sharding.
	// +kubebuilder:default=true
	// +optional
	ShardingEnabled *bool `json:"shardingEnabled,omitempty"`
}

// ZoneAwarenessSpec configures zone-aware replication for stateful components.
type ZoneAwarenessSpec struct {
	// Enabled enables zone-aware replication for ingesters and store-gateways.
	Enabled bool `json:"enabled"`

	// Zones is the list of availability zone names to distribute replicas across.
	// +kubebuilder:validation:MinItems=2
	Zones []string `json:"zones"`

	// TopologyKey is the node label key used to identify zones.
	// +kubebuilder:default="topology.kubernetes.io/zone"
	// +optional
	TopologyKey string `json:"topologyKey,omitempty"`
}

// GetTopologyKey returns the topology key, defaulting to "topology.kubernetes.io/zone".
func (z *ZoneAwarenessSpec) GetTopologyKey() string {
	if z == nil || z.TopologyKey == "" {
		return "topology.kubernetes.io/zone"
	}
	return z.TopologyKey
}

// ServiceMonitorSpec defines ServiceMonitor creation configuration.
type ServiceMonitorSpec struct {
	// Enabled enables ServiceMonitor creation.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Labels are additional labels for the ServiceMonitor.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// CompactorComponentSpec extends ComponentSpec with compactor-specific settings.
type CompactorComponentSpec struct {
	ComponentSpec `json:",inline"`

	// DataVolumeClaimSpec defines the PVC template for the compactor working directory.
	// +optional
	DataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec `json:"dataVolumeClaimSpec,omitempty"`
}

// CortexSpec defines the desired state of Cortex.
type CortexSpec struct {
	// Image defines the container image configuration.
	Image ImageSpec `json:"image"`

	// AuthEnabled enables multi-tenancy authentication.
	// +kubebuilder:default=true
	// +optional
	AuthEnabled *bool `json:"authEnabled,omitempty"`

	// Storage defines the object storage configuration.
	Storage StorageSpec `json:"storage"`

	// Ring defines the hash ring configuration.
	// +optional
	Ring *RingSpec `json:"ring,omitempty"`

	// Memberlist defines the memberlist gossip configuration.
	// +optional
	Memberlist *MemberlistSpec `json:"memberlist,omitempty"`

	// Limits defines global limits configuration.
	// +optional
	Limits *LimitsSpec `json:"limits,omitempty"`

	// RuntimeConfig references a ConfigMap with Cortex runtime config.
	// +optional
	RuntimeConfig *RuntimeConfigRef `json:"runtimeConfig,omitempty"`

	// ExternalConfig references an externally managed Cortex configuration.
	// When set, the operator will not generate config and will use this ConfigMap instead.
	// +optional
	ExternalConfig *ExternalConfigSpec `json:"externalConfig,omitempty"`

	// Distributor defines the distributor component configuration.
	// +optional
	Distributor *ComponentSpec `json:"distributor,omitempty"`

	// Ingester defines the ingester component configuration.
	// +optional
	Ingester *IngesterComponentSpec `json:"ingester,omitempty"`

	// Querier defines the querier component configuration.
	// +optional
	Querier *ComponentSpec `json:"querier,omitempty"`

	// QueryFrontend defines the query-frontend component configuration.
	// +optional
	QueryFrontend *ComponentSpec `json:"queryFrontend,omitempty"`

	// StoreGateway defines the store-gateway component configuration.
	// +optional
	StoreGateway *StoreGatewaySpec `json:"storeGateway,omitempty"`

	// Compactor defines the compactor component configuration.
	// +optional
	Compactor *CompactorComponentSpec `json:"compactor,omitempty"`

	// ZoneAwareness configures zone-aware replication for stateful components.
	// +optional
	ZoneAwareness *ZoneAwarenessSpec `json:"zoneAwareness,omitempty"`

	// ServiceMonitor defines ServiceMonitor configuration.
	// +optional
	ServiceMonitor *ServiceMonitorSpec `json:"serviceMonitor,omitempty"`
}

// ComponentStatus defines the observed status of a single component.
type ComponentStatus struct {
	// Replicas is the total desired replicas.
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready replicas.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// UpdatedReplicas is the number of updated replicas.
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
}

// CortexStatus defines the observed state of Cortex.
type CortexStatus struct {
	// Conditions represent the latest available observations of the Cortex state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Components contains the status of each Cortex component.
	// +optional
	Components map[string]ComponentStatus `json:"components,omitempty"`

	// ObservedGeneration is the most recent generation observed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Version is the Cortex image tag currently deployed.
	// +optional
	Version string `json:"version,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=`.metadata.creationTimestamp`

// Cortex is the Schema for the cortexes API.
type Cortex struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CortexSpec   `json:"spec,omitempty"`
	Status CortexStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CortexList contains a list of Cortex.
type CortexList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cortex `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cortex{}, &CortexList{})
}

// Helper methods

// GetReplicas returns the replicas for a ComponentSpec, defaulting to 1.
func (c *ComponentSpec) GetReplicas() int32 {
	if c == nil || c.Replicas == nil {
		return 1
	}
	return *c.Replicas
}

// GetReplicationFactor returns the replication factor, defaulting to 3.
func (r *RingSpec) GetReplicationFactor() int32 {
	if r == nil || r.ReplicationFactor == nil {
		return 3
	}
	return *r.ReplicationFactor
}

// GetNumTokens returns the number of tokens, defaulting to 512.
func (r *RingSpec) GetNumTokens() int32 {
	if r == nil || r.NumTokens == nil {
		return 512
	}
	return *r.NumTokens
}

// GetBindPort returns the memberlist bind port, defaulting to 7946.
func (m *MemberlistSpec) GetBindPort() int32 {
	if m == nil || m.BindPort == nil {
		return 7946
	}
	return *m.BindPort
}

// GetAbortIfJoinFails returns whether to abort if join fails, defaulting to false.
func (m *MemberlistSpec) GetAbortIfJoinFails() bool {
	if m == nil || m.AbortIfJoinFails == nil {
		return false
	}
	return *m.AbortIfJoinFails
}

// GetTerminationGracePeriodSeconds returns the ingester termination grace period.
func (i *IngesterComponentSpec) GetTerminationGracePeriodSeconds() int64 {
	if i == nil || i.TerminationGracePeriodSeconds == nil {
		return 2400
	}
	return *i.TerminationGracePeriodSeconds
}

// GetShardingEnabled returns whether store-gateway sharding is enabled.
func (s *StoreGatewaySpec) GetShardingEnabled() bool {
	if s == nil || s.ShardingEnabled == nil {
		return true
	}
	return *s.ShardingEnabled
}

// IsAuthEnabled returns whether multi-tenancy auth is enabled.
func (s *CortexSpec) IsAuthEnabled() bool {
	if s.AuthEnabled == nil {
		return true
	}
	return *s.AuthEnabled
}

// IsZoneAwarenessEnabled returns whether zone-aware replication is enabled.
func (s *CortexSpec) IsZoneAwarenessEnabled() bool {
	return s.ZoneAwareness != nil && s.ZoneAwareness.Enabled && len(s.ZoneAwareness.Zones) > 0
}

// GetDefaultDataVolumeClaimSpec returns a default PVC spec for stateful components.
func GetDefaultDataVolumeClaimSpec() corev1.PersistentVolumeClaimSpec {
	return corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("10Gi"),
			},
		},
	}
}
