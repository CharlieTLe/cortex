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

const (
	// DefaultHTTPPort is the default Cortex HTTP port.
	DefaultHTTPPort = 8080

	// DefaultGRPCPort is the default Cortex gRPC port.
	DefaultGRPCPort = 9095

	// DefaultMemberlistPort is the default memberlist gossip port.
	DefaultMemberlistPort = 7946

	// DefaultReplicationFactor is the default ring replication factor.
	DefaultReplicationFactor = 3

	// DefaultNumTokens is the default number of tokens per ingester.
	DefaultNumTokens = 512

	// DefaultTSDBDir is the default TSDB data directory.
	DefaultTSDBDir = "/data/tsdb"

	// DefaultConfigDir is the directory where config is mounted.
	DefaultConfigDir = "/etc/cortex"

	// DefaultConfigFile is the config file name.
	DefaultConfigFile = "cortex.yaml"

	// DefaultRuntimeConfigDir is the directory where runtime config is mounted.
	DefaultRuntimeConfigDir = "/etc/cortex/runtime"

	// DefaultImage is the default Cortex container image.
	DefaultImage = "quay.io/cortexproject/cortex"

	// DefaultPullPolicy is the default image pull policy.
	DefaultPullPolicy = "IfNotPresent"
)
