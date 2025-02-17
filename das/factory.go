// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package das

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"

	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/solgen/go/bridgegen"
	"github.com/offchainlabs/nitro/util/headerreader"
	"github.com/offchainlabs/nitro/util/signature"
)

// CreatePersistentStorageService creates any storage services that persist to files, database, cloud storage,
// and group them together into a RedundantStorage instance if there is more than one.
func CreatePersistentStorageService(
	ctx context.Context,
	config *DataAvailabilityConfig,
	syncFromStorageServices *[]*IterableStorageService,
	syncToStorageServices *[]StorageService,
) (StorageService, *LifecycleManager, error) {
	storageServices := make([]StorageService, 0, 10)
	var lifecycleManager LifecycleManager
	if config.LocalDBStorage.Enable {
		s, err := NewDBStorageService(ctx, config.LocalDBStorage.DataDir, config.LocalDBStorage.DiscardAfterTimeout)
		if err != nil {
			return nil, nil, err
		}
		if config.LocalDBStorage.SyncFromStorageService {
			iterableStorageService := NewIterableStorageService(ConvertStorageServiceToIterationCompatibleStorageService(s))
			*syncFromStorageServices = append(*syncFromStorageServices, iterableStorageService)
			s = iterableStorageService
		}
		if config.LocalDBStorage.SyncToStorageService {
			*syncToStorageServices = append(*syncToStorageServices, s)
		}
		lifecycleManager.Register(s)
		storageServices = append(storageServices, s)
	}

	if config.LocalFileStorage.Enable {
		s, err := NewLocalFileStorageService(config.LocalFileStorage.DataDir)
		if err != nil {
			return nil, nil, err
		}
		if config.LocalFileStorage.SyncFromStorageService {
			iterableStorageService := NewIterableStorageService(ConvertStorageServiceToIterationCompatibleStorageService(s))
			*syncFromStorageServices = append(*syncFromStorageServices, iterableStorageService)
			s = iterableStorageService
		}
		if config.LocalFileStorage.SyncToStorageService {
			*syncToStorageServices = append(*syncToStorageServices, s)
		}
		lifecycleManager.Register(s)
		storageServices = append(storageServices, s)
	}

	if config.S3Storage.Enable {
		s, err := NewS3StorageService(config.S3Storage)
		if err != nil {
			return nil, nil, err
		}
		lifecycleManager.Register(s)
		if config.S3Storage.SyncFromStorageService {
			iterableStorageService := NewIterableStorageService(ConvertStorageServiceToIterationCompatibleStorageService(s))
			*syncFromStorageServices = append(*syncFromStorageServices, iterableStorageService)
			s = iterableStorageService
		}
		if config.S3Storage.SyncToStorageService {
			*syncToStorageServices = append(*syncToStorageServices, s)
		}
		storageServices = append(storageServices, s)
	}

	if config.IpfsStorage.Enable {
		s, err := NewIpfsStorageService(ctx, config.IpfsStorage)
		if err != nil {
			return nil, nil, err
		}
		lifecycleManager.Register(s)
		storageServices = append(storageServices, s)
	}

	if len(storageServices) > 1 {
		s, err := NewRedundantStorageService(ctx, storageServices)
		if err != nil {
			return nil, nil, err
		}
		lifecycleManager.Register(s)
		return s, &lifecycleManager, nil
	}
	if len(storageServices) == 1 {
		return storageServices[0], &lifecycleManager, nil
	}
	return nil, &lifecycleManager, nil
}

func WrapStorageWithCache(
	ctx context.Context,
	config *DataAvailabilityConfig,
	storageService StorageService,
	syncFromStorageServices *[]*IterableStorageService,
	syncToStorageServices *[]StorageService,
	lifecycleManager *LifecycleManager) (StorageService, error) {
	if storageService == nil {
		return nil, nil
	}

	// Enable caches, Redis and (local) BigCache. Local is the outermost, so it will be tried first.
	var err error
	if config.RedisCache.Enable {
		storageService, err = NewRedisStorageService(config.RedisCache, storageService)
		lifecycleManager.Register(storageService)
		if err != nil {
			return nil, err
		}
		if config.RedisCache.SyncFromStorageService {
			iterableStorageService := NewIterableStorageService(ConvertStorageServiceToIterationCompatibleStorageService(storageService))
			*syncFromStorageServices = append(*syncFromStorageServices, iterableStorageService)
			storageService = iterableStorageService
		}
		if config.RedisCache.SyncToStorageService {
			*syncToStorageServices = append(*syncToStorageServices, storageService)
		}
	}
	if config.LocalCache.Enable {
		storageService, err = NewBigCacheStorageService(config.LocalCache, storageService)
		lifecycleManager.Register(storageService)
		if err != nil {
			return nil, err
		}
	}
	return storageService, nil
}

func CreateNearDAS(
	ctx context.Context,
	config *DataAvailabilityConfig,
	lifecycleManager *LifecycleManager,
) (DataAvailabilityServiceWriter, DataAvailabilityServiceReader, *NearService, error) {
	if !config.NEARAggregator.Enable {
		return nil, nil, nil, errors.New("--node.data-availability.near-aggregator.enable must be set")
	}

	log.Info("Initialising near service")
	nearSvc, err := NewNearService(*config)
	if err != nil {
		log.Error("initialising near service", "error", err)
		return nil, nil, nil, err
	}
	var nearWriter DataAvailabilityServiceWriter = nearSvc
	// log.Info("initialising near aggregator")
	//nearAggr, err = NewNearAggregator(ctx, *config, nearSvc)
	// if err != nil {
	// 	return nil, nil, err
	// }

	var nearReader DataAvailabilityServiceReader = nearSvc
	log.Info("initialising Chain Fetch Reader")
	// FIXME: lifecycleManager.Register(nearr)
	if err != nil {
		return nil, nil, nil, err
	}
	return nearWriter, nearReader, nearSvc, nil
}

func CreateBatchPosterDAS(
	ctx context.Context,
	config *DataAvailabilityConfig,
	dataSigner signature.DataSignerFunc,
	l1Reader arbutil.L1Interface,
	sequencerInboxAddr common.Address,
) (DataAvailabilityServiceWriter, DataAvailabilityServiceReader, *LifecycleManager, error) {
	if !config.Enable {
		return nil, nil, nil, nil
	}

	// Check config requirements
	if !config.RPCAggregator.Enable || !config.RestAggregator.Enable {
		return nil, nil, nil, errors.New("--node.data-availability.rpc-aggregator.enable and rest-aggregator.enable must be set when running a Batch Poster in AnyTrust mode")
	}

	if config.IpfsStorage.Enable {
		return nil, nil, nil, errors.New("--node.data-availability.ipfs-storage.enable may not be set when running a Nitro AnyTrust node in Batch Poster mode")
	}
	// Done checking config requirements

	var daWriter DataAvailabilityServiceWriter
	daWriter, err := NewRPCAggregator(ctx, *config)
	if err != nil {
		return nil, nil, nil, err
	}
	if dataSigner != nil {
		// In some tests the batch poster does not sign Store requests
		daWriter, err = NewStoreSigningDAS(daWriter, dataSigner)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	restAgg, err := NewRestfulClientAggregator(ctx, &config.RestAggregator)
	if err != nil {
		return nil, nil, nil, err
	}
	restAgg.Start(ctx)
	var lifecycleManager LifecycleManager
	lifecycleManager.Register(restAgg)
	var daReader DataAvailabilityServiceReader = restAgg
	daReader, err = NewChainFetchReader(daReader, l1Reader, sequencerInboxAddr)
	if err != nil {
		return nil, nil, nil, err
	}

	return daWriter, daReader, &lifecycleManager, nil
}

func CreateDAComponentsForDaserver(
	ctx context.Context,
	config *DataAvailabilityConfig,
	l1Reader *headerreader.HeaderReader,
	seqInboxAddress *common.Address,
) (DataAvailabilityServiceReader, DataAvailabilityServiceWriter, DataAvailabilityServiceHealthChecker, *LifecycleManager, error) {
	if !config.Enable {
		return nil, nil, nil, nil, nil
	}

	// Check config requirements
	if !config.LocalDBStorage.Enable &&
		!config.LocalFileStorage.Enable &&
		!config.S3Storage.Enable &&
		!config.IpfsStorage.Enable {
		return nil, nil, nil, nil, errors.New("At least one of --data-availability.(local-db-storage|local-file-storage|s3-storage|ipfs-storage) must be enabled.")
	}
	// Done checking config requirements

	var syncFromStorageServices []*IterableStorageService
	var syncToStorageServices []StorageService
	storageService, dasLifecycleManager, err := CreatePersistentStorageService(ctx, config, &syncFromStorageServices, &syncToStorageServices)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	storageService, err = WrapStorageWithCache(ctx, config, storageService, &syncFromStorageServices, &syncToStorageServices, dasLifecycleManager)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	var daWriter DataAvailabilityServiceWriter
	var daReader DataAvailabilityServiceReader = storageService
	var daHealthChecker DataAvailabilityServiceHealthChecker = storageService

	if config.NEARAggregator.Enable {
		log.Info("Enabling NEAR aggregator")
		w, r, svc, err := CreateNearDAS(ctx, config, dasLifecycleManager)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if config.NEARAggregator.StorageConfig.Enable {
			nearStorageService, err := NewNearStorageService(r, *svc, config.NEARAggregator.StorageConfig.DataDir)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			daReader = nearStorageService
			syncConf := config.NEARAggregator.StorageConfig.SyncToStorage
			var retentionPeriodSeconds uint64
			if uint64(syncConf.RetentionPeriod) == math.MaxUint64 {
				retentionPeriodSeconds = math.MaxUint64
			} else {
				retentionPeriodSeconds = uint64(syncConf.RetentionPeriod.Seconds())
			}

			if config.NEARAggregator.StorageConfig.SyncToStorage.Eager {
				if l1Reader == nil || seqInboxAddress == nil {
					return nil, nil, nil, nil, errors.New("l1-node-url and sequencer-inbox-address must be specified along with sync-to-storage.eager")
				}
				storageService, err = NewSyncingFallbackStorageService(
					ctx,
					nearStorageService,
					storageService,
					storageService,
					l1Reader,
					*seqInboxAddress,
					&syncConf)
				if err != nil {
					return nil, nil, nil, nil, err
				}
			} else {
				storageService = NewFallbackStorageService(nearStorageService, storageService, storageService,
					retentionPeriodSeconds, syncConf.IgnoreWriteErrors, true)
			}

			dasLifecycleManager.Register(storageService)
			if err != nil {
				return nil, nil, nil, nil, err
			}
		} else {
			daWriter = w
			daReader = r
		}
	} else {
		// The REST aggregator is used as the fallback if requested data is not present
		// in the storage service.
		if config.RestAggregator.Enable {
			restAgg, err := NewRestfulClientAggregator(ctx, &config.RestAggregator)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			restAgg.Start(ctx)
			dasLifecycleManager.Register(restAgg)

			syncConf := &config.RestAggregator.SyncToStorage
			var retentionPeriodSeconds uint64
			if uint64(syncConf.RetentionPeriod) == math.MaxUint64 {
				retentionPeriodSeconds = math.MaxUint64
			} else {
				retentionPeriodSeconds = uint64(syncConf.RetentionPeriod.Seconds())
			}

			if syncConf.Eager {
				if l1Reader == nil || seqInboxAddress == nil {
					return nil, nil, nil, nil, errors.New("l1-node-url and sequencer-inbox-address must be specified along with sync-to-storage.eager")
				}
				storageService, err = NewSyncingFallbackStorageService(
					ctx,
					storageService,
					restAgg,
					restAgg,
					l1Reader,
					*seqInboxAddress,
					syncConf)
				dasLifecycleManager.Register(storageService)
				if err != nil {
					return nil, nil, nil, nil, err
				}
			} else {
				storageService = NewFallbackStorageService(storageService, restAgg, restAgg,
					retentionPeriodSeconds, syncConf.IgnoreWriteErrors, true)
				dasLifecycleManager.Register(storageService)
			}

		}
	}

	if config.Key.KeyDir != "" || config.Key.PrivKey != "" {
		var seqInboxCaller *bridgegen.SequencerInboxCaller
		if seqInboxAddress != nil {
			seqInbox, err := bridgegen.NewSequencerInbox(*seqInboxAddress, (*l1Reader).Client())
			if err != nil {
				return nil, nil, nil, nil, err
			}

			seqInboxCaller = &seqInbox.SequencerInboxCaller
		}
		if config.DisableSignatureChecking {
			seqInboxCaller = nil
		}

		privKey, err := config.Key.BLSPrivKey()
		if err != nil {
			return nil, nil, nil, nil, err
		}

		daWriter, err = NewSignAfterStoreDASWriterWithSeqInboxCaller(
			privKey,
			seqInboxCaller,
			storageService,
			config.ExtraSignatureCheckingPublicKey,
		)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	log.Info("Da reader/writer", "reader", daReader, "writer", daWriter)

	if config.RegularSyncStorage.Enable && len(syncFromStorageServices) != 0 && len(syncToStorageServices) != 0 {
		regularlySyncStorage := NewRegularlySyncStorage(syncFromStorageServices, syncToStorageServices, config.RegularSyncStorage)
		regularlySyncStorage.Start(ctx)
	}

	if seqInboxAddress != nil {
		daReader, err = NewChainFetchReader(daReader, (*l1Reader).Client(), *seqInboxAddress)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}

	return daReader, daWriter, daHealthChecker, dasLifecycleManager, nil
}

func CreateDAReaderForNode(
	ctx context.Context,
	config *DataAvailabilityConfig,
	l1Reader *headerreader.HeaderReader,
	seqInboxAddress *common.Address,
) (DataAvailabilityServiceReader, *LifecycleManager, error) {
	if !config.Enable {
		return nil, nil, nil
	}

	// Check config requirements
	if config.RPCAggregator.Enable {
		return nil, nil, errors.New("node.data-availability.rpc-aggregator is only for Batch Poster mode")
	}

	if !config.NEARAggregator.Enable {
		if !config.RestAggregator.Enable && !config.IpfsStorage.Enable {
			return nil, nil, fmt.Errorf("--node.data-availability.enable was set but neither of --node.data-availability.(rest-aggregator|ipfs-storage) were enabled. When running a Nitro Anytrust node in non-Batch Poster mode, some way to get the batch data is required.")
		}
	}

	if config.RestAggregator.SyncToStorage.Eager {
		return nil, nil, errors.New("--node.data-availability.rest-aggregator.sync-to-storage.eager can't be used with a Nitro node, only lazy syncing can be used.")
	}
	// Done checking config requirements

	storageService, dasLifecycleManager, err := CreatePersistentStorageService(ctx, config, nil, nil)
	if err != nil {
		return nil, nil, err
	}

	var daReader DataAvailabilityServiceReader
	if config.NEARAggregator.Enable {
		log.Info("Initialising near service")
		_, r, _, err := CreateNearDAS(ctx, config, dasLifecycleManager)
		if err != nil {
			return nil, nil, err
		}

		if storageService != nil {
			syncConf := &config.RestAggregator.SyncToStorage
			var retentionPeriodSeconds uint64
			if uint64(syncConf.RetentionPeriod) == math.MaxUint64 {
				retentionPeriodSeconds = math.MaxUint64
			} else {
				retentionPeriodSeconds = uint64(syncConf.RetentionPeriod.Seconds())
			}

			// This falls back to REST and updates the local IPFS repo if the data is found.
			nearSvc, err := NewNearService(*config)
			if err != nil {
				log.Error("initialising near service", "error", err)
				return nil, nil, err
			}
			// TODO: hack fix
			storageService = NewFallbackStorageService(storageService, r, nearSvc,
				retentionPeriodSeconds, syncConf.IgnoreWriteErrors, true)
			dasLifecycleManager.Register(storageService)

			daReader = storageService
		} else {
			daReader = r
		}
	} else if config.RestAggregator.Enable {
		var restAgg *SimpleDASReaderAggregator
		restAgg, err = NewRestfulClientAggregator(ctx, &config.RestAggregator)
		if err != nil {
			return nil, nil, err
		}
		restAgg.Start(ctx)
		dasLifecycleManager.Register(restAgg)

		if storageService != nil {
			syncConf := &config.RestAggregator.SyncToStorage
			var retentionPeriodSeconds uint64
			if uint64(syncConf.RetentionPeriod) == math.MaxUint64 {
				retentionPeriodSeconds = math.MaxUint64
			} else {
				retentionPeriodSeconds = uint64(syncConf.RetentionPeriod.Seconds())
			}

			// This falls back to REST and updates the local IPFS repo if the data is found.
			storageService = NewFallbackStorageService(storageService, restAgg, restAgg,
				retentionPeriodSeconds, syncConf.IgnoreWriteErrors, true)
			dasLifecycleManager.Register(storageService)

			daReader = storageService
		} else {
			daReader = restAgg
		}
	}

	if seqInboxAddress != nil {
		seqInbox, err := bridgegen.NewSequencerInbox(*seqInboxAddress, (*l1Reader).Client())
		if err != nil {
			return nil, nil, err
		}
		daReader, err = NewChainFetchReaderWithSeqInbox(daReader, seqInbox)
		if err != nil {
			return nil, nil, err
		}
	}

	return daReader, dasLifecycleManager, nil
}
