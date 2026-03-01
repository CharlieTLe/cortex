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
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"

	cortexv1alpha1 "github.com/cortexproject/cortex/operator/api/v1alpha1"
)

// Builder generates Cortex YAML configuration from a CRD spec.
type Builder struct {
	name      string
	namespace string
	spec      *cortexv1alpha1.CortexSpec
}

// NewBuilder creates a new config builder.
func NewBuilder(name, namespace string, spec *cortexv1alpha1.CortexSpec) *Builder {
	return &Builder{
		name:      name,
		namespace: namespace,
		spec:      spec,
	}
}

// Build generates the Cortex configuration YAML.
func (b *Builder) Build() (string, error) {
	cfg := make(map[string]interface{})

	cfg["target"] = "all"
	cfg["auth_enabled"] = b.spec.IsAuthEnabled()

	// Server config
	cfg["server"] = b.buildServer()

	// Memberlist config
	cfg["memberlist"] = b.buildMemberlist()

	// Distributor config
	cfg["distributor"] = b.buildDistributor()

	// Ingester config
	cfg["ingester"] = b.buildIngester()

	// Ingester client config
	cfg["ingester_client"] = b.buildIngesterClient()

	// Blocks storage config
	cfg["blocks_storage"] = b.buildBlocksStorage()

	// Store gateway config
	cfg["store_gateway"] = b.buildStoreGateway()

	// Compactor config
	cfg["compactor"] = b.buildCompactor()

	// Frontend worker config
	cfg["frontend_worker"] = b.buildFrontendWorker()

	// Limits config
	if b.spec.Limits != nil {
		cfg["limits"] = b.buildLimits()
	}

	// Runtime config
	if b.spec.RuntimeConfig != nil {
		cfg["runtime_config"] = b.buildRuntimeConfig()
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshaling config: %w", err)
	}
	return string(data), nil
}

func (b *Builder) buildServer() map[string]interface{} {
	return map[string]interface{}{
		"http_listen_port": DefaultHTTPPort,
		"grpc_listen_port": DefaultGRPCPort,
	}
}

func (b *Builder) buildMemberlist() map[string]interface{} {
	bindPort := int32(DefaultMemberlistPort)
	abortIfJoinFails := false

	if b.spec.Memberlist != nil {
		bindPort = b.spec.Memberlist.GetBindPort()
		abortIfJoinFails = b.spec.Memberlist.GetAbortIfJoinFails()
	}

	joinAddr := fmt.Sprintf("dns+%s-gossip.%s.svc.cluster.local:%d", b.name, b.namespace, bindPort)

	return map[string]interface{}{
		"join_members":                []string{joinAddr},
		"abort_if_cluster_join_fails": abortIfJoinFails,
		"bind_port":                   bindPort,
	}
}

func (b *Builder) buildDistributor() map[string]interface{} {
	return map[string]interface{}{
		"ring": map[string]interface{}{
			"kvstore": map[string]interface{}{
				"store": "memberlist",
			},
		},
	}
}

func (b *Builder) buildIngester() map[string]interface{} {
	replicationFactor := int32(DefaultReplicationFactor)
	numTokens := int32(DefaultNumTokens)

	if b.spec.Ring != nil {
		replicationFactor = b.spec.Ring.GetReplicationFactor()
		numTokens = b.spec.Ring.GetNumTokens()
	}

	ring := map[string]interface{}{
		"replication_factor": replicationFactor,
		"kvstore": map[string]interface{}{
			"store": "memberlist",
		},
	}

	lifecycler := map[string]interface{}{
		"join_after":     "0s",
		"num_tokens":     numTokens,
		"observe_period": "5s",
		"ring":           ring,
	}

	if b.spec.IsZoneAwarenessEnabled() {
		lifecycler["availability_zone"] = "${CORTEX_AVAILABILITY_ZONE}"
		ring["zone_awareness_enabled"] = true
	}

	return map[string]interface{}{
		"lifecycler": lifecycler,
	}
}

func (b *Builder) buildIngesterClient() map[string]interface{} {
	return map[string]interface{}{
		"grpc_client_config": map[string]interface{}{
			"max_recv_msg_size": 104857600,
			"max_send_msg_size": 104857600,
		},
	}
}

func (b *Builder) buildBlocksStorage() map[string]interface{} {
	storage := b.buildStorageBackend()

	return map[string]interface{}{
		"backend": string(b.spec.Storage.Backend),
		"tsdb": map[string]interface{}{
			"dir": DefaultTSDBDir,
		},
		string(b.spec.Storage.Backend): storage,
	}
}

func (b *Builder) buildStorageBackend() map[string]interface{} {
	switch b.spec.Storage.Backend {
	case cortexv1alpha1.StorageBackendS3:
		if b.spec.Storage.S3 == nil {
			return map[string]interface{}{}
		}
		s3 := map[string]interface{}{
			"bucket_name": b.spec.Storage.S3.BucketName,
		}
		if b.spec.Storage.S3.Endpoint != "" {
			s3["endpoint"] = b.spec.Storage.S3.Endpoint
		}
		if b.spec.Storage.S3.Region != "" {
			s3["region"] = b.spec.Storage.S3.Region
		}
		if b.spec.Storage.S3.Insecure {
			s3["insecure"] = true
		}
		return s3

	case cortexv1alpha1.StorageBackendGCS:
		if b.spec.Storage.GCS == nil {
			return map[string]interface{}{}
		}
		return map[string]interface{}{
			"bucket_name": b.spec.Storage.GCS.BucketName,
		}

	case cortexv1alpha1.StorageBackendAzure:
		if b.spec.Storage.Azure == nil {
			return map[string]interface{}{}
		}
		azure := map[string]interface{}{
			"container_name": b.spec.Storage.Azure.ContainerName,
		}
		if b.spec.Storage.Azure.AccountName != "" {
			azure["account_name"] = b.spec.Storage.Azure.AccountName
		}
		return azure

	case cortexv1alpha1.StorageBackendSwift:
		if b.spec.Storage.Swift == nil {
			return map[string]interface{}{}
		}
		swift := map[string]interface{}{
			"container_name": b.spec.Storage.Swift.ContainerName,
		}
		if b.spec.Storage.Swift.AuthURL != "" {
			swift["auth_url"] = b.spec.Storage.Swift.AuthURL
		}
		return swift
	}

	return map[string]interface{}{}
}

func (b *Builder) buildStoreGateway() map[string]interface{} {
	shardingEnabled := true
	if b.spec.StoreGateway != nil {
		shardingEnabled = b.spec.StoreGateway.GetShardingEnabled()
	}

	shardingRing := map[string]interface{}{
		"kvstore": map[string]interface{}{
			"store": "memberlist",
		},
	}

	if b.spec.IsZoneAwarenessEnabled() {
		shardingRing["instance_availability_zone"] = "${CORTEX_AVAILABILITY_ZONE}"
		shardingRing["zone_awareness_enabled"] = true
	}

	return map[string]interface{}{
		"sharding_enabled": shardingEnabled,
		"sharding_ring":    shardingRing,
	}
}

func (b *Builder) buildCompactor() map[string]interface{} {
	// Note: The compactor's sharding_ring in Cortex does not support
	// instance_availability_zone or zone_awareness_enabled fields (unlike
	// ingester and store-gateway). Zone-aware deployment is handled at the
	// Kubernetes level via per-zone StatefulSets, but the ring config
	// cannot include these fields.
	return map[string]interface{}{
		"sharding_ring": map[string]interface{}{
			"kvstore": map[string]interface{}{
				"store": "memberlist",
			},
		},
	}
}

func (b *Builder) buildFrontendWorker() map[string]interface{} {
	frontendAddr := fmt.Sprintf("%s-query-frontend-headless.%s.svc.cluster.local:%d", b.name, b.namespace, DefaultGRPCPort)
	return map[string]interface{}{
		"frontend_address": frontendAddr,
	}
}

func (b *Builder) buildLimits() map[string]interface{} {
	limits := make(map[string]interface{})

	if b.spec.Limits.IngestionRate != nil {
		limits["ingestion_rate"] = *b.spec.Limits.IngestionRate
	}
	if b.spec.Limits.IngestionBurstSize != nil {
		limits["ingestion_burst_size"] = *b.spec.Limits.IngestionBurstSize
	}
	if b.spec.Limits.MaxSeriesPerUser != nil {
		limits["max_series_per_user"] = *b.spec.Limits.MaxSeriesPerUser
	}
	if b.spec.Limits.MaxSeriesPerMetric != nil {
		limits["max_series_per_metric"] = *b.spec.Limits.MaxSeriesPerMetric
	}
	if b.spec.Limits.MaxGlobalSeriesPerUser != nil {
		limits["max_global_series_per_user"] = *b.spec.Limits.MaxGlobalSeriesPerUser
	}
	if b.spec.Limits.MaxGlobalSeriesPerMetric != nil {
		limits["max_global_series_per_metric"] = *b.spec.Limits.MaxGlobalSeriesPerMetric
	}

	return limits
}

func (b *Builder) buildRuntimeConfig() map[string]interface{} {
	key := "runtime.yaml"
	if b.spec.RuntimeConfig.Key != "" {
		key = b.spec.RuntimeConfig.Key
	}

	return map[string]interface{}{
		"file": filepath.Join(DefaultRuntimeConfigDir, key),
	}
}
