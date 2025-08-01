package ring

// Based on https://raw.githubusercontent.com/stathat/consistent/master/consistent.go

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/cortexproject/cortex/pkg/ring/kv"
	shardUtil "github.com/cortexproject/cortex/pkg/ring/shard"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/cortexproject/cortex/pkg/util/flagext"
	"github.com/cortexproject/cortex/pkg/util/services"
)

const (
	unhealthy = "Unhealthy"

	// GetBufferSize is the suggested size of buffers passed to Ring.Get(). It's based on
	// a typical replication factor 3, plus extra room for a JOINING + LEAVING instance.
	GetBufferSize = 5

	// GetZoneSize is the suggested size of zone map passed to Ring.Get(). It's based on
	// a typical replication factor 3.
	GetZoneSize = 3
)

// ReadRing represents the read interface to the ring.
type ReadRing interface {

	// Get returns n (or more) instances which form the replicas for the given key.
	// bufDescs, bufHosts and bufZones are slices to be overwritten for the return value
	// to avoid memory allocation; can be nil, or created with ring.MakeBuffersForGet().
	Get(key uint32, op Operation, bufDescs []InstanceDesc, bufHosts []string, bufZones map[string]int) (ReplicationSet, error)

	// GetAllHealthy returns all healthy instances in the ring, for the given operation.
	// This function doesn't check if the quorum is honored, so doesn't fail if the number
	// of unhealthy instances is greater than the tolerated max unavailable.
	GetAllHealthy(op Operation) (ReplicationSet, error)

	// GetAllInstanceDescs returns a slice of healthy and unhealthy InstanceDesc.
	GetAllInstanceDescs(op Operation) ([]InstanceDesc, []InstanceDesc, error)

	// GetInstanceDescsForOperation returns map of InstanceDesc with instance ID as the keys.
	GetInstanceDescsForOperation(op Operation) (map[string]InstanceDesc, error)

	// GetReplicationSetForOperation returns all instances where the input operation should be executed.
	// The resulting ReplicationSet doesn't necessarily contains all healthy instances
	// in the ring, but could contain the minimum set of instances required to execute
	// the input operation.
	GetReplicationSetForOperation(op Operation) (ReplicationSet, error)

	ReplicationFactor() int

	// InstancesCount returns the number of instances in the ring.
	InstancesCount() int

	// ShuffleShard returns a subring for the provided identifier (eg. a tenant ID)
	// and size (number of instances).
	ShuffleShard(identifier string, size int) ReadRing

	// ShuffleShardWithZoneStability does the same as ShuffleShard but using a different shuffle sharding algorithm.
	// It doesn't round up shard size to be divisible to number of zones and make sure when scaling up/down one
	// shard size at a time, at most 1 instance can be changed.
	// It is only used in Store Gateway for now.
	ShuffleShardWithZoneStability(identifier string, size int) ReadRing

	// GetInstanceState returns the current state of an instance or an error if the
	// instance does not exist in the ring.
	GetInstanceState(instanceID string) (InstanceState, error)

	// GetInstanceIdByAddr returns the instance id from its address or an error if the
	//	// instance does not exist in the ring.
	GetInstanceIdByAddr(addr string) (string, error)

	// ShuffleShardWithLookback is like ShuffleShard() but the returned subring includes
	// all instances that have been part of the identifier's shard since "now - lookbackPeriod".
	ShuffleShardWithLookback(identifier string, size int, lookbackPeriod time.Duration, now time.Time) ReadRing

	// HasInstance returns whether the ring contains an instance matching the provided instanceID.
	HasInstance(instanceID string) bool

	// CleanupShuffleShardCache should delete cached shuffle-shard subrings for given identifier.
	CleanupShuffleShardCache(identifier string)
}

var (
	// Write operation that also extends replica set, if instance state is not ACTIVE.
	Write = NewOp([]InstanceState{ACTIVE}, func(s InstanceState) bool {
		// We do not want to Write to instances that are not ACTIVE, but we do want
		// to write the extra replica somewhere.  So we increase the size of the set
		// of replicas for the key.
		// NB unhealthy instances will be filtered later by defaultReplicationStrategy.Filter().
		return s != ACTIVE
	})

	// WriteNoExtend is like Write, but with no replicaset extension.
	WriteNoExtend = NewOp([]InstanceState{ACTIVE}, func(s InstanceState) bool {
		// We want to skip instances that are READONLY. So we will increase the size of replication
		// for the key
		return s == READONLY
	})

	// Read operation that extends the replica set if an instance is not ACTIVE, PENDING, LEAVING, JOINING OR READONLY
	Read = NewOp([]InstanceState{ACTIVE, PENDING, LEAVING, JOINING, READONLY}, func(s InstanceState) bool {
		// To match Write with extended replica set we have to also increase the
		// size of the replica set for Read, but we can read from LEAVING ingesters.
		return s != ACTIVE && s != LEAVING && s != JOINING && s != READONLY
	})

	// Reporting is a special value for inquiring about health.
	Reporting = allStatesRingOperation
)

var (
	// ErrEmptyRing is the error returned when trying to get an element when nothing has been added to hash.
	ErrEmptyRing = errors.New("empty ring")

	// ErrInstanceNotFound is the error returned when trying to get information for an instance
	// not registered within the ring.
	ErrInstanceNotFound = errors.New("instance not found in the ring")

	// ErrTooManyUnhealthyInstances is the error returned when there are too many failed instances for a
	// specific operation.
	ErrTooManyUnhealthyInstances = errors.New("too many unhealthy instances in the ring")

	// ErrInconsistentTokensInfo is the error returned if, due to an internal bug, the mapping between
	// a token and its own instance is missing or unknown.
	ErrInconsistentTokensInfo = errors.New("inconsistent ring tokens information")
)

// Config for a Ring
type Config struct {
	KVStore                kv.Config              `yaml:"kvstore"`
	HeartbeatTimeout       time.Duration          `yaml:"heartbeat_timeout"`
	ReplicationFactor      int                    `yaml:"replication_factor"`
	ZoneAwarenessEnabled   bool                   `yaml:"zone_awareness_enabled"`
	ExcludedZones          flagext.StringSliceCSV `yaml:"excluded_zones"`
	DetailedMetricsEnabled bool                   `yaml:"detailed_metrics_enabled"`

	// Whether the shuffle-sharding subring cache is disabled. This option is set
	// internally and never exposed to the user.
	SubringCacheDisabled bool `yaml:"-"`
}

// RegisterFlags adds the flags required to config this to the given FlagSet with a specified prefix
func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	cfg.RegisterFlagsWithPrefix("", f)
}

// RegisterFlagsWithPrefix adds the flags required to config this to the given FlagSet with a specified prefix
func (cfg *Config) RegisterFlagsWithPrefix(prefix string, f *flag.FlagSet) {
	cfg.KVStore.RegisterFlagsWithPrefix(prefix, "collectors/", f)

	f.DurationVar(&cfg.HeartbeatTimeout, prefix+"ring.heartbeat-timeout", time.Minute, "The heartbeat timeout after which ingesters are skipped for reads/writes. 0 = never (timeout disabled).")
	f.BoolVar(&cfg.DetailedMetricsEnabled, prefix+"ring.detailed-metrics-enabled", true, "Set to true to enable ring detailed metrics. These metrics provide detailed information, such as token count and ownership per tenant. Disabling them can significantly decrease the number of metrics emitted by the distributors.")
	f.IntVar(&cfg.ReplicationFactor, prefix+"distributor.replication-factor", 3, "The number of ingesters to write to and read from.")
	f.BoolVar(&cfg.ZoneAwarenessEnabled, prefix+"distributor.zone-awareness-enabled", false, "True to enable the zone-awareness and replicate ingested samples across different availability zones.")
	f.Var(&cfg.ExcludedZones, prefix+"distributor.excluded-zones", "Comma-separated list of zones to exclude from the ring. Instances in excluded zones will be filtered out from the ring.")
}

type instanceInfo struct {
	InstanceID string
	Zone       string
}

// Ring holds the information about the members of the consistent hash ring.
type Ring struct {
	services.Service

	key      string
	cfg      Config
	KVClient kv.Client
	strategy ReplicationStrategy

	mtx              sync.RWMutex
	ringDesc         *Desc
	ringTokens       []uint32
	ringTokensByZone map[string][]uint32

	// Maps a token with the information of the instance holding it. This map is immutable and
	// cannot be chanced in place because it's shared "as is" between subrings (the only way to
	// change it is to create a new one and replace it).
	ringInstanceByToken map[uint32]instanceInfo

	ringInstanceIdByAddr map[string]string

	// When did a set of instances change the last time (instance changing state or heartbeat is ignored for this timestamp).
	lastTopologyChange time.Time

	// List of zones for which there's at least 1 instance in the ring. This list is guaranteed
	// to be sorted alphabetically.
	ringZones         []string
	previousRingZones []string

	// Cache of shuffle-sharded subrings per identifier. Invalidated when topology changes.
	// If set to nil, no caching is done (used by tests, and subrings).
	shuffledSubringCache map[subringCacheKey]*Ring

	memberOwnershipGaugeVec *prometheus.GaugeVec
	numMembersGaugeVec      *prometheus.GaugeVec
	totalTokensGauge        prometheus.Gauge
	numTokensGaugeVec       *prometheus.GaugeVec
	oldestTimestampGaugeVec *prometheus.GaugeVec
	reportedOwners          map[string]struct{}

	logger log.Logger
}

type subringCacheKey struct {
	identifier string
	shardSize  int

	zoneStableSharding bool
}

// New creates a new Ring. Being a service, Ring needs to be started to do anything.
func New(cfg Config, name, key string, logger log.Logger, reg prometheus.Registerer) (*Ring, error) {
	codec := GetCodec()
	// Suffix all client names with "-ring" to denote this kv client is used by the ring
	store, err := kv.NewClient(
		cfg.KVStore,
		codec,
		kv.RegistererWithKVName(reg, name+"-ring"),
		logger,
	)
	if err != nil {
		return nil, err
	}

	return NewWithStoreClientAndStrategy(cfg, name, key, store, NewDefaultReplicationStrategy(), reg, logger)
}

func NewWithStoreClientAndStrategy(cfg Config, name, key string, store kv.Client, strategy ReplicationStrategy, reg prometheus.Registerer, logger log.Logger) (*Ring, error) {
	if cfg.ReplicationFactor <= 0 {
		return nil, fmt.Errorf("ReplicationFactor must be greater than zero: %d", cfg.ReplicationFactor)
	}

	r := &Ring{
		key:                  key,
		cfg:                  cfg,
		KVClient:             store,
		strategy:             strategy,
		ringDesc:             &Desc{},
		shuffledSubringCache: map[subringCacheKey]*Ring{},
		memberOwnershipGaugeVec: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name:        "ring_member_ownership_percent",
			Help:        "The percent ownership of the ring by member",
			ConstLabels: map[string]string{"name": name}},
			[]string{"member"}),
		numMembersGaugeVec: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name:        "ring_members",
			Help:        "Number of members in the ring",
			ConstLabels: map[string]string{"name": name}},
			[]string{"state", "zone"}),
		totalTokensGauge: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Name:        "ring_tokens_total",
			Help:        "Number of tokens in the ring",
			ConstLabels: map[string]string{"name": name}}),
		numTokensGaugeVec: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name:        "ring_tokens_owned",
			Help:        "The number of tokens in the ring owned by the member",
			ConstLabels: map[string]string{"name": name}},
			[]string{"member"}),
		oldestTimestampGaugeVec: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name:        "ring_oldest_member_timestamp",
			Help:        "Timestamp of the oldest member in the ring.",
			ConstLabels: map[string]string{"name": name}},
			[]string{"state"}),
		logger: logger,
	}

	r.Service = services.NewBasicService(r.starting, r.loop, nil).WithName(fmt.Sprintf("%s ring client", name))
	return r, nil
}

func (r *Ring) starting(ctx context.Context) error {
	// Get the initial ring state so that, as soon as the service will be running, the in-memory
	// ring would be already populated and there's no race condition between when the service is
	// running and the WatchKey() callback is called for the first time.
	value, err := r.KVClient.Get(ctx, r.key)
	if err != nil {
		return errors.Wrap(err, "unable to initialise ring state")
	}
	if value != nil {
		r.updateRingState(value.(*Desc))
	} else {
		level.Info(r.logger).Log("msg", "ring doesn't exist in KV store yet")
	}
	return nil
}

func (r *Ring) loop(ctx context.Context) error {
	// Update the ring metrics at start of the main loop.
	r.mtx.Lock()
	r.updateRingMetrics(Different)
	r.mtx.Unlock()

	r.KVClient.WatchKey(ctx, r.key, func(value interface{}) bool {
		if value == nil {
			level.Info(r.logger).Log("msg", "ring doesn't exist in KV store yet")
			return true
		}

		r.updateRingState(value.(*Desc))
		return true
	})
	return nil
}

func (r *Ring) updateRingState(ringDesc *Desc) {
	r.mtx.RLock()
	prevRing := r.ringDesc
	r.mtx.RUnlock()

	// Filter out all instances belonging to excluded zones.
	if len(r.cfg.ExcludedZones) > 0 {
		for instanceID, instance := range ringDesc.Ingesters {
			if util.StringsContain(r.cfg.ExcludedZones, instance.Zone) {
				delete(ringDesc.Ingesters, instanceID)
			}
		}
	}

	rc := prevRing.RingCompare(ringDesc)
	if rc == Equal || rc == EqualButStatesAndTimestamps || rc == EqualButReadOnly {
		// No need to update tokens or zones. Only states and timestamps
		// have changed. (If Equal, nothing has changed, but that doesn't happen
		// when watching the ring for updates).
		r.mtx.Lock()
		r.ringDesc = ringDesc
		if rc == EqualButReadOnly && r.shuffledSubringCache != nil {
			// Invalidate all cached subrings.
			r.shuffledSubringCache = make(map[subringCacheKey]*Ring)
		}
		r.updateRingMetrics(rc)
		r.mtx.Unlock()
		return
	}

	now := time.Now()
	ringTokens := ringDesc.GetTokens()
	ringTokensByZone := ringDesc.getTokensByZone()
	ringInstanceByToken := ringDesc.getTokensInfo()
	ringInstanceByAddr := ringDesc.getInstancesByAddr()
	ringZones := getZones(ringTokensByZone)

	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.ringDesc = ringDesc
	r.ringTokens = ringTokens
	r.ringTokensByZone = ringTokensByZone
	r.ringInstanceByToken = ringInstanceByToken
	r.ringInstanceIdByAddr = ringInstanceByAddr
	r.previousRingZones = r.ringZones
	r.ringZones = ringZones
	r.lastTopologyChange = now
	if r.shuffledSubringCache != nil {
		// Invalidate all cached subrings.
		r.shuffledSubringCache = make(map[subringCacheKey]*Ring)
	}
	r.updateRingMetrics(rc)
}

// Get returns n (or more) instances which form the replicas for the given key.
// This implementation guarantees:
// - Stability: given the same ring, two invocations returns the same set for same operation.
// - Consistency: adding/removing 1 instance from the ring returns set with no more than 1 difference for same operation.
func (r *Ring) Get(key uint32, op Operation, bufDescs []InstanceDesc, bufHosts []string, bufZones map[string]int) (ReplicationSet, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()
	if r.ringDesc == nil || len(r.ringTokens) == 0 {
		return ReplicationSet{}, ErrEmptyRing
	}

	var (
		replicationFactor      = r.cfg.ReplicationFactor
		instances              = bufDescs[:0]
		start                  = searchToken(r.ringTokens, key)
		iterations             = 0
		maxInstancePerZone     = replicationFactor / len(r.ringZones)
		zonesWithExtraInstance = replicationFactor % len(r.ringZones)

		// We use a slice instead of a map because it's faster to search within a
		// slice than lookup a map for a very low number of items.
		distinctHosts       = bufHosts[:0]
		numOfInstanceByZone = resetZoneMap(bufZones)
	)

	for i := start; len(distinctHosts) < replicationFactor && iterations < len(r.ringTokens); i++ {
		iterations++
		// Wrap i around in the ring.
		i %= len(r.ringTokens)
		token := r.ringTokens[i]

		info, ok := r.ringInstanceByToken[token]
		if !ok {
			// This should never happen unless a bug in the ring code.
			return ReplicationSet{}, ErrInconsistentTokensInfo
		}

		// We want n *distinct* instances.
		if util.StringsContain(distinctHosts, info.InstanceID) {
			continue
		}

		// Ignore if the instances don't have a zone set.
		if r.cfg.ZoneAwarenessEnabled && info.Zone != "" {
			maxNumOfInstance := maxInstancePerZone
			// If we still have room for zones with extra instance, increase the instance threshold by 1
			if zonesWithExtraInstance > 0 {
				maxNumOfInstance++
			}

			if numOfInstanceByZone[info.Zone] >= maxNumOfInstance {
				continue
			}
		}

		distinctHosts = append(distinctHosts, info.InstanceID)
		instance := r.ringDesc.Ingesters[info.InstanceID]

		// Check whether the replica set should be extended given we're including
		// this instance.
		if op.ShouldExtendReplicaSetOnState(instance.State) {
			replicationFactor++
		} else if r.cfg.ZoneAwarenessEnabled && info.Zone != "" {
			// We should only add the zone if we are not going to extend,
			// as we want to extend the instance in the same AZ.
			if numOfInstance, ok := numOfInstanceByZone[info.Zone]; !ok {
				numOfInstanceByZone[info.Zone] = 1
			} else if numOfInstance < maxInstancePerZone {
				numOfInstanceByZone[info.Zone]++
			} else {
				// This zone will have an extra instance
				numOfInstanceByZone[info.Zone]++
				zonesWithExtraInstance--
			}
		}

		instances = append(instances, instance)
	}

	healthyInstances, maxFailure, err := r.strategy.Filter(instances, op, r.cfg.ReplicationFactor, r.cfg.HeartbeatTimeout, r.cfg.ZoneAwarenessEnabled, r.KVClient.LastUpdateTime(r.key))
	if err != nil {
		return ReplicationSet{}, err
	}

	return ReplicationSet{
		Instances: healthyInstances,
		MaxErrors: maxFailure,
	}, nil
}

// GetAllHealthy implements ReadRing.
func (r *Ring) GetAllHealthy(op Operation) (ReplicationSet, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	if r.ringDesc == nil || len(r.ringDesc.Ingesters) == 0 {
		return ReplicationSet{}, ErrEmptyRing
	}

	storageLastUpdate := r.KVClient.LastUpdateTime(r.key)
	instances := make([]InstanceDesc, 0, len(r.ringDesc.Ingesters))
	for _, instance := range r.ringDesc.Ingesters {
		if r.IsHealthy(&instance, op, storageLastUpdate) {
			instances = append(instances, instance)
		}
	}

	return ReplicationSet{
		Instances: instances,
		MaxErrors: 0,
	}, nil
}

// GetAllInstanceDescs implements ReadRing.
func (r *Ring) GetAllInstanceDescs(op Operation) ([]InstanceDesc, []InstanceDesc, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	if r.ringDesc == nil || len(r.ringDesc.Ingesters) == 0 {
		return nil, nil, ErrEmptyRing
	}
	healthyInstances := make([]InstanceDesc, 0, len(r.ringDesc.Ingesters))
	unhealthyInstances := make([]InstanceDesc, 0, len(r.ringDesc.Ingesters))
	storageLastUpdate := r.KVClient.LastUpdateTime(r.key)
	for _, instance := range r.ringDesc.Ingesters {
		if r.IsHealthy(&instance, op, storageLastUpdate) {
			healthyInstances = append(healthyInstances, instance)
		} else {
			unhealthyInstances = append(unhealthyInstances, instance)
		}
	}

	return healthyInstances, unhealthyInstances, nil
}

// GetInstanceDescsForOperation implements ReadRing.
func (r *Ring) GetInstanceDescsForOperation(op Operation) (map[string]InstanceDesc, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	if r.ringDesc == nil || len(r.ringDesc.Ingesters) == 0 {
		return map[string]InstanceDesc{}, ErrEmptyRing
	}

	storageLastUpdate := r.KVClient.LastUpdateTime(r.key)
	instanceDescs := make(map[string]InstanceDesc, 0)
	for id, instance := range r.ringDesc.Ingesters {
		if r.IsHealthy(&instance, op, storageLastUpdate) {
			instanceDescs[id] = instance
		}
	}

	return instanceDescs, nil
}

// GetReplicationSetForOperation implements ReadRing.
func (r *Ring) GetReplicationSetForOperation(op Operation) (ReplicationSet, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	if r.ringDesc == nil || len(r.ringTokens) == 0 {
		return ReplicationSet{}, ErrEmptyRing
	}

	// Build the initial replication set, excluding unhealthy instances.
	healthyInstances := make([]InstanceDesc, 0, len(r.ringDesc.Ingesters))
	zoneFailures := make(map[string]struct{})
	storageLastUpdate := r.KVClient.LastUpdateTime(r.key)

	for _, instance := range r.ringDesc.Ingesters {
		if r.IsHealthy(&instance, op, storageLastUpdate) {
			healthyInstances = append(healthyInstances, instance)
		} else {
			zoneFailures[instance.Zone] = struct{}{}
		}
	}

	// Max errors and max unavailable zones are mutually exclusive. We initialise both
	// to 0 and then we update them whether zone-awareness is enabled or not.
	maxErrors := 0
	maxUnavailableZones := 0

	if r.cfg.ZoneAwarenessEnabled {
		// Given data is replicated to RF different zones, we can tolerate a number of
		// RF/2 failing zones. However, we need to protect from the case the ring currently
		// contains instances in a number of zones < RF.
		numReplicatedZones := min(len(r.ringZones), r.cfg.ReplicationFactor)
		minSuccessZones := (numReplicatedZones / 2) + 1
		maxUnavailableZones = minSuccessZones - 1

		if len(zoneFailures) > maxUnavailableZones {
			return ReplicationSet{}, ErrTooManyUnhealthyInstances
		}

		if len(zoneFailures) > 0 {
			// We remove all instances (even healthy ones) from zones with at least
			// 1 failing instance. Due to how replication works when zone-awareness is
			// enabled (data is replicated to RF different zones), there's no benefit in
			// querying healthy instances from "failing zones". A zone is considered
			// failed if there is single error.
			filteredInstances := make([]InstanceDesc, 0, len(r.ringDesc.Ingesters))
			for _, instance := range healthyInstances {
				if _, ok := zoneFailures[instance.Zone]; !ok {
					filteredInstances = append(filteredInstances, instance)
				}
			}

			healthyInstances = filteredInstances
		}

		// Since we removed all instances from zones containing at least 1 failing
		// instance, we have to decrease the max unavailable zones accordingly.
		maxUnavailableZones -= len(zoneFailures)
	} else {
		// Calculate the number of required instances;
		// ensure we always require at least RF-1 when RF=3.
		numRequired := len(r.ringDesc.Ingesters)
		if numRequired < r.cfg.ReplicationFactor {
			numRequired = r.cfg.ReplicationFactor
		}
		// We can tolerate this many failures
		numRequired -= r.cfg.ReplicationFactor / 2

		if len(healthyInstances) < numRequired {
			return ReplicationSet{}, ErrTooManyUnhealthyInstances
		}

		maxErrors = len(healthyInstances) - numRequired
	}

	return ReplicationSet{
		Instances:           healthyInstances,
		MaxErrors:           maxErrors,
		MaxUnavailableZones: maxUnavailableZones,
	}, nil
}

func (r *Ring) countTokensByAz() (map[string]map[string]uint32, map[string]int64) {
	numTokens := map[string]map[string]uint32{}
	owned := map[string]int64{}

	for zone, zonalTokens := range r.ringDesc.getTokensByZone() {
		numTokens[zone] = map[string]uint32{}
		for i := 1; i <= len(zonalTokens); i++ {
			index := i % len(zonalTokens)
			diff := tokenDistance(zonalTokens[i-1], zonalTokens[index])
			info := r.ringInstanceByToken[zonalTokens[index]]
			owned[info.InstanceID] = owned[info.InstanceID] + diff
			numTokens[zone][info.InstanceID] = numTokens[zone][info.InstanceID] + 1
		}
	}

	// Set to 0 the number of owned tokens by instances which don't have tokens yet.
	for id, info := range r.ringDesc.Ingesters {
		if _, ok := owned[id]; !ok {
			owned[id] = 0
			numTokens[info.Zone][id] = 0
		}
	}

	return numTokens, owned
}

// countTokens returns the number of tokens and tokens within the range for each instance.
// The ring read lock must be already taken when calling this function.
func (r *Ring) countTokens() (map[string]uint32, map[string]int64) {
	owned := map[string]int64{}
	numTokens := map[string]uint32{}
	for i := 1; i <= len(r.ringTokens); i++ { // Compute how many tokens are within the range.
		index := i % len(r.ringTokens)
		diff := tokenDistance(r.ringTokens[i-1], r.ringTokens[index])

		info := r.ringInstanceByToken[r.ringTokens[index]]
		numTokens[info.InstanceID] = numTokens[info.InstanceID] + 1
		owned[info.InstanceID] = owned[info.InstanceID] + diff
	}

	// Set to 0 the number of owned tokens by instances which don't have tokens yet.
	for id := range r.ringDesc.Ingesters {
		if _, ok := owned[id]; !ok {
			owned[id] = 0
			numTokens[id] = 0
		}
	}

	return numTokens, owned
}

// updateRingMetrics updates ring metrics. Caller must be holding the Write lock!
func (r *Ring) updateRingMetrics(compareResult CompareResult) {
	if compareResult == Equal {
		return
	}

	numByStateByZone := map[string]map[string]int{}
	oldestTimestampByState := map[string]int64{}

	// Initialized to zero so we emit zero-metrics (instead of not emitting anything)
	for _, s := range []string{unhealthy, ACTIVE.String(), LEAVING.String(), PENDING.String(), JOINING.String(), READONLY.String()} {
		numByStateByZone[s] = map[string]int{}
		// make sure removed zones got zero value
		for _, zone := range r.previousRingZones {
			numByStateByZone[s][zone] = 0
		}
		for _, zone := range r.ringZones {
			numByStateByZone[s][zone] = 0
		}
		oldestTimestampByState[s] = 0
	}

	for _, instance := range r.ringDesc.Ingesters {
		s := instance.State.String()
		if !r.IsHealthy(&instance, Reporting, r.KVClient.LastUpdateTime(r.key)) {
			s = unhealthy
		}
		if _, ok := numByStateByZone[s]; !ok {
			numByStateByZone[s] = map[string]int{}
		}
		numByStateByZone[s][instance.Zone]++
		if oldestTimestampByState[s] == 0 || instance.Timestamp < oldestTimestampByState[s] {
			oldestTimestampByState[s] = instance.Timestamp
		}
	}

	for state, zones := range numByStateByZone {
		for zone, count := range zones {
			r.numMembersGaugeVec.WithLabelValues(state, zone).Set(float64(count))
		}
	}
	for state, timestamp := range oldestTimestampByState {
		r.oldestTimestampGaugeVec.WithLabelValues(state).Set(float64(timestamp))
	}

	if compareResult == EqualButStatesAndTimestamps {
		return
	}

	if r.cfg.DetailedMetricsEnabled {
		prevOwners := r.reportedOwners
		r.reportedOwners = make(map[string]struct{})
		numTokens, ownedRange := r.countTokens()
		for id, totalOwned := range ownedRange {
			r.memberOwnershipGaugeVec.WithLabelValues(id).Set(float64(totalOwned) / float64(math.MaxUint32+1))
			r.numTokensGaugeVec.WithLabelValues(id).Set(float64(numTokens[id]))
			delete(prevOwners, id)
			r.reportedOwners[id] = struct{}{}
		}

		for k := range prevOwners {
			r.memberOwnershipGaugeVec.DeleteLabelValues(k)
			r.numTokensGaugeVec.DeleteLabelValues(k)
		}
	}

	r.totalTokensGauge.Set(float64(len(r.ringTokens)))
}

// ShuffleShard returns a subring for the provided identifier (eg. a tenant ID)
// and size (number of instances). The size is expected to be a multiple of the
// number of zones and the returned subring will contain the same number of
// instances per zone as far as there are enough registered instances in the ring.
//
// The algorithm used to build the subring is a shuffle sharder based on probabilistic
// hashing. We treat each zone as a separate ring and pick N unique replicas from each
// zone, walking the ring starting from random but predictable numbers. The random
// generator is initialised with a seed based on the provided identifier.
//
// This implementation guarantees:
//
// - Stability: given the same ring, two invocations returns the same result.
//
// - Consistency: adding/removing 1 instance from the ring generates a resulting
// subring with no more than 1 difference.
//
// - Shuffling: probabilistically, for a large enough cluster each identifier gets a different
// set of instances, with a reduced number of overlapping instances between two identifiers.
func (r *Ring) ShuffleShard(identifier string, size int) ReadRing {
	return r.shuffleShardWithCache(identifier, size, false)
}

func (r *Ring) ShuffleShardWithZoneStability(identifier string, size int) ReadRing {
	return r.shuffleShardWithCache(identifier, size, true)
}

// ShuffleShardWithLookback is like ShuffleShard() but the returned subring includes all instances
// that have been part of the identifier's shard since "now - lookbackPeriod".
//
// The returned subring may be unbalanced with regard to zones and should never be used for write
// operations (read only).
//
// This function doesn't support caching.
func (r *Ring) ShuffleShardWithLookback(identifier string, size int, lookbackPeriod time.Duration, now time.Time) ReadRing {
	// Nothing to do if the shard size is not smaller than the actual ring.
	if size <= 0 || r.InstancesCount() <= size {
		return r
	}

	return r.shuffleShard(identifier, size, lookbackPeriod, now, false)
}

func (r *Ring) shuffleShardWithCache(identifier string, size int, zoneStableSharding bool) ReadRing {
	// Nothing to do if the shard size is not smaller than the actual ring.
	if size <= 0 || r.InstancesCount() <= size {
		return r
	}

	if cached := r.getCachedShuffledSubring(identifier, size, zoneStableSharding); cached != nil {
		return cached
	}

	result := r.shuffleShard(identifier, size, 0, time.Now(), zoneStableSharding)

	r.setCachedShuffledSubring(identifier, size, zoneStableSharding, result)
	return result
}

func (r *Ring) shuffleShard(identifier string, size int, lookbackPeriod time.Duration, now time.Time, zoneStableSharding bool) *Ring {
	lookbackUntil := now.Add(-lookbackPeriod).Unix()

	r.mtx.RLock()
	defer r.mtx.RUnlock()

	var (
		numInstancesPerZone    int
		actualZones            []string
		zonesWithExtraInstance int
	)

	if r.cfg.ZoneAwarenessEnabled {
		if zoneStableSharding {
			numInstancesPerZone = size / len(r.ringZones)
			zonesWithExtraInstance = size % len(r.ringZones)
		} else {
			numInstancesPerZone = shardUtil.ShuffleShardExpectedInstancesPerZone(size, len(r.ringZones))
		}
		actualZones = r.ringZones
	} else {
		numInstancesPerZone = size
		actualZones = []string{""}
	}

	shard := make(map[string]InstanceDesc, size)

	// We need to iterate zones always in the same order to guarantee stability.
	for _, zone := range actualZones {
		var tokens []uint32

		if r.cfg.ZoneAwarenessEnabled {
			tokens = r.ringTokensByZone[zone]
		} else {
			// When zone-awareness is disabled, we just iterate over 1 single fake zone
			// and use all tokens in the ring.
			tokens = r.ringTokens
		}

		// Initialise the random generator used to select instances in the ring.
		// Since we consider each zone like an independent ring, we have to use dedicated
		// pseudo-random generator for each zone, in order to guarantee the "consistency"
		// property when the shard size changes or a new zone is added.
		random := rand.New(rand.NewSource(shardUtil.ShuffleShardSeed(identifier, zone)))

		// To select one more instance while guaranteeing the "consistency" property,
		// we do pick a random value from the generator and resolve uniqueness collisions
		// (if any) continuing walking the ring.
		finalInstancesPerZone := numInstancesPerZone
		if zonesWithExtraInstance > 0 {
			zonesWithExtraInstance--
			finalInstancesPerZone++
		}
		for i := 0; i < finalInstancesPerZone; i++ {
			start := searchToken(tokens, random.Uint32())
			iterations := 0
			found := false

			for p := start; iterations < len(tokens); p++ {
				iterations++

				// Wrap p around in the ring.
				p %= len(tokens)

				info, ok := r.ringInstanceByToken[tokens[p]]
				if !ok {
					// This should never happen unless a bug in the ring code.
					panic(ErrInconsistentTokensInfo)
				}

				// Ensure we select an unique instance.
				if _, ok := shard[info.InstanceID]; ok {
					continue
				}

				instanceID := info.InstanceID
				instance := r.ringDesc.Ingesters[instanceID]
				shard[instanceID] = instance

				// If the lookback is enabled and this instance has been registered within the lookback period
				// then we should include it in the subring but continuing selecting instances.
				// If an instance is in READONLY we should always extend. The write path will filter it out when GetRing.
				// The read path should extend to get new ingester used on write
				if (lookbackPeriod > 0 && instance.RegisteredTimestamp >= lookbackUntil) || instance.State == READONLY {
					continue
				}

				found = true
				break
			}

			// If one more instance has not been found, we can stop looking for
			// more instances in this zone, because it means the zone has no more
			// instances which haven't been already selected.
			if !found {
				break
			}
		}
	}

	// Build a read-only ring for the shard.
	shardDesc := &Desc{Ingesters: shard}
	shardTokensByZone := shardDesc.getTokensByZone()

	return &Ring{
		cfg:              r.cfg,
		strategy:         r.strategy,
		ringDesc:         shardDesc,
		ringTokens:       shardDesc.GetTokens(),
		ringTokensByZone: shardTokensByZone,
		ringZones:        getZones(shardTokensByZone),
		KVClient:         r.KVClient,

		// We reference the original map as is in order to avoid copying. It's safe to do
		// because this map is immutable by design and it's a superset of the actual instances
		// with the subring.
		ringInstanceByToken: r.ringInstanceByToken,

		// For caching to work, remember these values.
		lastTopologyChange: r.lastTopologyChange,
	}
}

// GetInstanceState returns the current state of an instance or an error if the
// instance does not exist in the ring.
func (r *Ring) GetInstanceState(instanceID string) (InstanceState, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	instances := r.ringDesc.GetIngesters()
	instance, ok := instances[instanceID]
	if !ok {
		return PENDING, ErrInstanceNotFound
	}

	return instance.GetState(), nil
}

// GetInstanceIdByAddr implements ReadRing.
func (r *Ring) GetInstanceIdByAddr(addr string) (string, error) {
	if i, ok := r.ringInstanceIdByAddr[addr]; ok {
		return i, nil
	}

	return "notFound", ErrInstanceNotFound
}

// HasInstance returns whether the ring contains an instance matching the provided instanceID.
func (r *Ring) HasInstance(instanceID string) bool {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	instances := r.ringDesc.GetIngesters()
	_, ok := instances[instanceID]
	return ok
}

func (r *Ring) getCachedShuffledSubring(identifier string, size int, zoneStableSharding bool) *Ring {
	if r.cfg.SubringCacheDisabled {
		return nil
	}

	r.mtx.RLock()
	defer r.mtx.RUnlock()

	// if shuffledSubringCache map is nil, reading it returns default value (nil pointer).
	cached := r.shuffledSubringCache[subringCacheKey{identifier: identifier, shardSize: size, zoneStableSharding: zoneStableSharding}]
	if cached == nil {
		return nil
	}

	cached.mtx.Lock()
	defer cached.mtx.Unlock()

	// Update instance states and timestamps. We know that the topology is the same,
	// so zones and tokens are equal.
	for name, cachedIng := range cached.ringDesc.Ingesters {
		ing := r.ringDesc.Ingesters[name]
		cachedIng.State = ing.State
		cachedIng.Timestamp = ing.Timestamp
		cached.ringDesc.Ingesters[name] = cachedIng
	}
	return cached
}

func (r *Ring) setCachedShuffledSubring(identifier string, size int, zoneStableSharding bool, subring *Ring) {
	if subring == nil || r.cfg.SubringCacheDisabled {
		return
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()

	// Only cache if *this* ring hasn't changed since computing result
	// (which can happen between releasing the read lock and getting read-write lock).
	// Note that shuffledSubringCache can be only nil when set by test.
	if r.shuffledSubringCache != nil && r.lastTopologyChange.Equal(subring.lastTopologyChange) {
		r.shuffledSubringCache[subringCacheKey{identifier: identifier, shardSize: size, zoneStableSharding: zoneStableSharding}] = subring
	}
}

func (r *Ring) CleanupShuffleShardCache(identifier string) {
	if r.cfg.SubringCacheDisabled {
		return
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()

	for k := range r.shuffledSubringCache {
		if k.identifier == identifier {
			delete(r.shuffledSubringCache, k)
		}
	}
}

// Operation describes which instances can be included in the replica set, based on their state.
//
// Implemented as bitmap, with upper 16-bits used for encoding extendReplicaSet, and lower 16-bits used for encoding healthy states.
type Operation uint32

// NewOp constructs new Operation with given "healthy" states for operation, and optional function to extend replica set.
// Result of calling shouldExtendReplicaSet is cached.
func NewOp(healthyStates []InstanceState, shouldExtendReplicaSet func(s InstanceState) bool) Operation {
	op := Operation(0)
	for _, s := range healthyStates {
		op |= (1 << s)
	}

	if shouldExtendReplicaSet != nil {
		for _, s := range []InstanceState{ACTIVE, LEAVING, PENDING, JOINING, LEFT, READONLY} {
			if shouldExtendReplicaSet(s) {
				op |= (0x10000 << s)
			}
		}
	}

	return op
}

// IsInstanceInStateHealthy is used during "filtering" phase to remove undesired instances based on their state.
func (op Operation) IsInstanceInStateHealthy(s InstanceState) bool {
	return op&(1<<s) > 0
}

// ShouldExtendReplicaSetOnState returns true if given a state of instance that's going to be
// added to the replica set, the replica set size should be extended by 1
// more instance for the given operation.
func (op Operation) ShouldExtendReplicaSetOnState(s InstanceState) bool {
	return op&(0x10000<<s) > 0
}

// All states are healthy, no states extend replica set.
var allStatesRingOperation = Operation(0x0000ffff)

func AutoForgetFromRing(ringDesc *Desc, forgetPeriod time.Duration, logger log.Logger) {
	for id, instance := range ringDesc.Ingesters {
		lastHeartbeat := time.Unix(instance.GetTimestamp(), 0)

		if time.Since(lastHeartbeat) > forgetPeriod {
			level.Warn(logger).Log("msg", "auto-forgetting instance from the ring because it is unhealthy for a long time", "instance", id, "last_heartbeat", lastHeartbeat.String(), "forget_period", forgetPeriod)
			ringDesc.RemoveIngester(id)
		}
	}
}
