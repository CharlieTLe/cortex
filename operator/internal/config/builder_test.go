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

package config

import (
	"testing"

	"gopkg.in/yaml.v3"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

func ptr[T any](v T) *T {
	return &v
}

func TestBuildDefaultConfig(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{
			Tag: "v1.17.0",
		},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3: &cortexv1alpha1.S3StorageSpec{
				BucketName: "cortex-blocks",
				Endpoint:   "s3.amazonaws.com",
				Region:     "us-east-1",
			},
		},
	}

	b := NewBuilder("test-cortex", "cortex-system", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal generated config: %v", err)
	}

	// auth_enabled should default to true
	if v, ok := cfg["auth_enabled"].(bool); !ok || !v {
		t.Errorf("expected auth_enabled=true, got %v", cfg["auth_enabled"])
	}

	// server ports
	server := cfg["server"].(map[string]interface{})
	if v := server["http_listen_port"]; v != DefaultHTTPPort {
		t.Errorf("expected http_listen_port=%d, got %v", DefaultHTTPPort, v)
	}
	if v := server["grpc_listen_port"]; v != DefaultGRPCPort {
		t.Errorf("expected grpc_listen_port=%d, got %v", DefaultGRPCPort, v)
	}

	// memberlist join address
	memberlist := cfg["memberlist"].(map[string]interface{})
	joinMembers := memberlist["join_members"].([]interface{})
	expected := "dns+test-cortex-gossip.cortex-system.svc.cluster.local:7946"
	if joinMembers[0] != expected {
		t.Errorf("expected join_members=%q, got %q", expected, joinMembers[0])
	}

	// ingester ring config
	ingester := cfg["ingester"].(map[string]interface{})
	lifecycler := ingester["lifecycler"].(map[string]interface{})
	if v := lifecycler["num_tokens"]; v != DefaultNumTokens {
		t.Errorf("expected num_tokens=%d, got %v", DefaultNumTokens, v)
	}
	ring := lifecycler["ring"].(map[string]interface{})
	if v := ring["replication_factor"]; v != DefaultReplicationFactor {
		t.Errorf("expected replication_factor=%d, got %v", DefaultReplicationFactor, v)
	}

	// blocks_storage with S3
	blocks := cfg["blocks_storage"].(map[string]interface{})
	if v := blocks["backend"]; v != "s3" {
		t.Errorf("expected backend=s3, got %v", v)
	}
	s3Config := blocks["s3"].(map[string]interface{})
	if v := s3Config["bucket_name"]; v != "cortex-blocks" {
		t.Errorf("expected bucket_name=cortex-blocks, got %v", v)
	}

	// frontend_worker
	fw := cfg["frontend_worker"].(map[string]interface{})
	expectedFW := "test-cortex-query-frontend-headless.cortex-system.svc.cluster.local:9095"
	if v := fw["frontend_address"]; v != expectedFW {
		t.Errorf("expected frontend_address=%q, got %q", expectedFW, v)
	}
}

func TestBuildWithCustomRing(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
		Ring: &cortexv1alpha1.RingSpec{
			ReplicationFactor: ptr(int32(5)),
			NumTokens:         ptr(int32(256)),
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	ingester := cfg["ingester"].(map[string]interface{})
	lifecycler := ingester["lifecycler"].(map[string]interface{})
	ring := lifecycler["ring"].(map[string]interface{})

	if v := ring["replication_factor"]; v != 5 {
		t.Errorf("expected replication_factor=5, got %v", v)
	}
	if v := lifecycler["num_tokens"]; v != 256 {
		t.Errorf("expected num_tokens=256, got %v", v)
	}
}

func TestBuildWithGCS(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendGCS,
			GCS:     &cortexv1alpha1.GCSStorageSpec{BucketName: "my-gcs-bucket"},
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	blocks := cfg["blocks_storage"].(map[string]interface{})
	if v := blocks["backend"]; v != "gcs" {
		t.Errorf("expected backend=gcs, got %v", v)
	}
	gcs := blocks["gcs"].(map[string]interface{})
	if v := gcs["bucket_name"]; v != "my-gcs-bucket" {
		t.Errorf("expected bucket_name=my-gcs-bucket, got %v", v)
	}
}

func TestBuildWithAzure(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendAzure,
			Azure: &cortexv1alpha1.AzureStorageSpec{
				ContainerName: "my-container",
				AccountName:   "myaccount",
			},
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	blocks := cfg["blocks_storage"].(map[string]interface{})
	if v := blocks["backend"]; v != "azure" {
		t.Errorf("expected backend=azure, got %v", v)
	}
	azure := blocks["azure"].(map[string]interface{})
	if v := azure["container_name"]; v != "my-container" {
		t.Errorf("expected container_name=my-container, got %v", v)
	}
	if v := azure["account_name"]; v != "myaccount" {
		t.Errorf("expected account_name=myaccount, got %v", v)
	}
}

func TestBuildWithLimits(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
		Limits: &cortexv1alpha1.LimitsSpec{
			IngestionRate:      ptr(float64(25000)),
			IngestionBurstSize: ptr(int32(50000)),
			MaxSeriesPerUser:   ptr(int64(5000000)),
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	limits := cfg["limits"].(map[string]interface{})
	// yaml.v3 may unmarshal whole-number floats as int
	switch v := limits["ingestion_rate"].(type) {
	case float64:
		if v != 25000 {
			t.Errorf("expected ingestion_rate=25000, got %v", v)
		}
	case int:
		if v != 25000 {
			t.Errorf("expected ingestion_rate=25000, got %v", v)
		}
	default:
		t.Errorf("expected ingestion_rate=25000, got %v (%T)", limits["ingestion_rate"], limits["ingestion_rate"])
	}
	if v := limits["ingestion_burst_size"]; v != 50000 {
		t.Errorf("expected ingestion_burst_size=50000, got %v", v)
	}
	if v := limits["max_series_per_user"]; v != 5000000 {
		t.Errorf("expected max_series_per_user=5000000, got %v", v)
	}
}

func TestBuildWithRuntimeConfig(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
		RuntimeConfig: &cortexv1alpha1.RuntimeConfigRef{
			Name: "runtime-config",
			Key:  "runtime.yaml",
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	rc := cfg["runtime_config"].(map[string]interface{})
	if v := rc["file"]; v != "/etc/cortex/runtime/runtime.yaml" {
		t.Errorf("expected file=/etc/cortex/runtime/runtime.yaml, got %v", v)
	}
}

func TestBuildAuthDisabled(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image:       cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		AuthEnabled: ptr(false),
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if v, ok := cfg["auth_enabled"].(bool); !ok || v {
		t.Errorf("expected auth_enabled=false, got %v", cfg["auth_enabled"])
	}
}

func TestBuildWithCustomMemberlist(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
		Memberlist: &cortexv1alpha1.MemberlistSpec{
			BindPort:         ptr(int32(8000)),
			AbortIfJoinFails: ptr(true),
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	memberlist := cfg["memberlist"].(map[string]interface{})
	if v := memberlist["bind_port"]; v != 8000 {
		t.Errorf("expected bind_port=8000, got %v", v)
	}
	if v := memberlist["abort_if_cluster_join_fails"]; v != true {
		t.Errorf("expected abort_if_cluster_join_fails=true, got %v", v)
	}

	joinMembers := memberlist["join_members"].([]interface{})
	expected := "dns+test-gossip.ns.svc.cluster.local:8000"
	if joinMembers[0] != expected {
		t.Errorf("expected join_members=%q, got %q", expected, joinMembers[0])
	}
}

func TestBuildWithZoneAwareness(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
		ZoneAwareness: &cortexv1alpha1.ZoneAwarenessSpec{
			Enabled:     true,
			Zones:       []string{"us-east-1a", "us-east-1b", "us-east-1c"},
			TopologyKey: "topology.kubernetes.io/zone",
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Ingester should have zone awareness config.
	ingester := cfg["ingester"].(map[string]interface{})
	lifecycler := ingester["lifecycler"].(map[string]interface{})

	if v := lifecycler["availability_zone"]; v != "${CORTEX_AVAILABILITY_ZONE}" {
		t.Errorf("expected availability_zone=${CORTEX_AVAILABILITY_ZONE}, got %v", v)
	}

	ring := lifecycler["ring"].(map[string]interface{})
	if v := ring["zone_awareness_enabled"]; v != true {
		t.Errorf("expected zone_awareness_enabled=true, got %v", v)
	}

	// Store gateway should have zone awareness config.
	sg := cfg["store_gateway"].(map[string]interface{})
	shardingRing := sg["sharding_ring"].(map[string]interface{})

	if v := shardingRing["instance_availability_zone"]; v != "${CORTEX_AVAILABILITY_ZONE}" {
		t.Errorf("expected instance_availability_zone=${CORTEX_AVAILABILITY_ZONE}, got %v", v)
	}
	if v := shardingRing["zone_awareness_enabled"]; v != true {
		t.Errorf("expected zone_awareness_enabled=true, got %v", v)
	}

	// Compactor sharding_ring should NOT have zone awareness fields
	// (Cortex compactor.RingConfig does not support them).
	compactor := cfg["compactor"].(map[string]interface{})
	compactorShardingRing := compactor["sharding_ring"].(map[string]interface{})

	if _, ok := compactorShardingRing["instance_availability_zone"]; ok {
		t.Error("expected no compactor instance_availability_zone (compactor ring does not support zone awareness)")
	}
	if _, ok := compactorShardingRing["zone_awareness_enabled"]; ok {
		t.Error("expected no compactor zone_awareness_enabled (compactor ring does not support zone awareness)")
	}
}

func TestBuildWithoutZoneAwareness(t *testing.T) {
	spec := &cortexv1alpha1.CortexSpec{
		Image: cortexv1alpha1.ImageSpec{Tag: "v1.17.0"},
		Storage: cortexv1alpha1.StorageSpec{
			Backend: cortexv1alpha1.StorageBackendS3,
			S3:      &cortexv1alpha1.S3StorageSpec{BucketName: "test"},
		},
	}

	b := NewBuilder("test", "ns", spec)
	configYAML, err := b.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Ingester should NOT have zone awareness config.
	ingester := cfg["ingester"].(map[string]interface{})
	lifecycler := ingester["lifecycler"].(map[string]interface{})

	if _, ok := lifecycler["availability_zone"]; ok {
		t.Error("expected no availability_zone when zone awareness is disabled")
	}

	ring := lifecycler["ring"].(map[string]interface{})
	if _, ok := ring["zone_awareness_enabled"]; ok {
		t.Error("expected no zone_awareness_enabled when zone awareness is disabled")
	}

	// Store gateway should NOT have zone awareness config.
	sg := cfg["store_gateway"].(map[string]interface{})
	shardingRing := sg["sharding_ring"].(map[string]interface{})

	if _, ok := shardingRing["instance_availability_zone"]; ok {
		t.Error("expected no instance_availability_zone when zone awareness is disabled")
	}
	if _, ok := shardingRing["zone_awareness_enabled"]; ok {
		t.Error("expected no zone_awareness_enabled when zone awareness is disabled")
	}

	// Compactor should NOT have zone awareness config.
	compactor := cfg["compactor"].(map[string]interface{})
	compactorShardingRing := compactor["sharding_ring"].(map[string]interface{})

	if _, ok := compactorShardingRing["instance_availability_zone"]; ok {
		t.Error("expected no compactor instance_availability_zone when zone awareness is disabled")
	}
	if _, ok := compactorShardingRing["zone_awareness_enabled"]; ok {
		t.Error("expected no compactor zone_awareness_enabled when zone awareness is disabled")
	}
}
