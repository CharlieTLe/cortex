package querier

import (
	"context"
	"time"

	"github.com/go-kit/log"
	"github.com/oklog/ulid/v2"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/thanos-io/objstore"

	"github.com/cortexproject/cortex/pkg/util/validation"

	"github.com/cortexproject/cortex/pkg/storage/bucket"
	"github.com/cortexproject/cortex/pkg/storage/tsdb/bucketindex"
	"github.com/cortexproject/cortex/pkg/util/services"
)

var (
	errBucketIndexBlocksFinderNotRunning = errors.New("bucket index blocks finder is not running")
	errBucketIndexTooOld                 = errors.New("bucket index is too old and the last time it was updated exceeds the allowed max staleness")
)

type BucketIndexBlocksFinderConfig struct {
	IndexLoader              bucketindex.LoaderConfig
	MaxStalePeriod           time.Duration
	IgnoreDeletionMarksDelay time.Duration
	IgnoreBlocksWithin       time.Duration
}

// BucketIndexBlocksFinder implements BlocksFinder interface and find blocks in the bucket
// looking up the bucket index.
type BucketIndexBlocksFinder struct {
	services.Service

	cfg    BucketIndexBlocksFinderConfig
	loader *bucketindex.Loader
}

func NewBucketIndexBlocksFinder(cfg BucketIndexBlocksFinderConfig, bkt objstore.Bucket, cfgProvider bucket.TenantConfigProvider, logger log.Logger, reg prometheus.Registerer) *BucketIndexBlocksFinder {
	loader := bucketindex.NewLoader(cfg.IndexLoader, bkt, cfgProvider, logger, reg)

	return &BucketIndexBlocksFinder{
		cfg:     cfg,
		loader:  loader,
		Service: loader,
	}
}

// GetBlocks implements BlocksFinder.
func (f *BucketIndexBlocksFinder) GetBlocks(ctx context.Context, userID string, minT, maxT int64) (bucketindex.Blocks, map[ulid.ULID]*bucketindex.BlockDeletionMark, error) {
	if f.State() != services.Running {
		return nil, nil, errBucketIndexBlocksFinderNotRunning
	}
	if maxT < minT {
		return nil, nil, errInvalidBlocksRange
	}

	// Get the bucket index for this user.
	idx, ss, err := f.loader.GetIndex(ctx, userID)
	if errors.Is(err, bucketindex.ErrIndexNotFound) {
		// This is a legit edge case, happening when a new tenant has not shipped blocks to the storage yet
		// so the bucket index hasn't been created yet.
		return nil, nil, nil
	} else if errors.Is(err, bucket.ErrCustomerManagedKeyAccessDenied) {
		return nil, nil, validation.AccessDeniedError(err.Error())
	}

	// Short circuit when bucket failed to be updated due CMK errors recently
	if time.Since(ss.GetNonQueryableUntil()) < 0 && ss.NonQueryableReason == bucketindex.CustomerManagedKeyError {
		return nil, nil, validation.AccessDeniedError(bucket.ErrCustomerManagedKeyAccessDenied.Error())
	}

	if err != nil {
		return nil, nil, err
	}

	// Ensure the bucket index is not too old.
	if time.Since(idx.GetUpdatedAt()) > f.cfg.MaxStalePeriod {
		return nil, nil, errBucketIndexTooOld
	}

	var (
		matchingBlocks        = map[ulid.ULID]*bucketindex.Block{}
		matchingDeletionMarks = map[ulid.ULID]*bucketindex.BlockDeletionMark{}
	)

	// Filter blocks containing samples within the range.
	for _, block := range idx.Blocks {
		if !block.Within(minT, maxT) {
			continue
		}

		matchingBlocks[block.ID] = block
	}

	for _, mark := range idx.BlockDeletionMarks {
		// Filter deletion marks by matching blocks only.
		if _, ok := matchingBlocks[mark.ID]; !ok {
			continue
		}

		// Exclude blocks marked for deletion. This is the same logic as Thanos IgnoreDeletionMarkFilter.
		if time.Since(time.Unix(mark.DeletionTime, 0)).Seconds() > f.cfg.IgnoreDeletionMarksDelay.Seconds() {
			delete(matchingBlocks, mark.ID)
			continue
		}

		matchingDeletionMarks[mark.ID] = mark
	}

	// Convert matching blocks into a list.
	blocks := make(bucketindex.Blocks, 0, len(matchingBlocks))
	for _, b := range matchingBlocks {
		blocks = append(blocks, b)
	}

	return blocks, matchingDeletionMarks, nil
}
