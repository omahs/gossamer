// Copyright 2021 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package dot

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ChainSafe/chaindb"
	"github.com/ChainSafe/gossamer/dot/core"
	"github.com/ChainSafe/gossamer/dot/digest"
	"github.com/ChainSafe/gossamer/dot/network"
	"github.com/ChainSafe/gossamer/dot/rpc"
	"github.com/ChainSafe/gossamer/dot/rpc/modules"
	"github.com/ChainSafe/gossamer/dot/state"
	"github.com/ChainSafe/gossamer/dot/sync"
	"github.com/ChainSafe/gossamer/dot/system"
	"github.com/ChainSafe/gossamer/dot/types"
	"github.com/ChainSafe/gossamer/internal/log"
	"github.com/ChainSafe/gossamer/internal/metrics"
	"github.com/ChainSafe/gossamer/internal/pprof"
	"github.com/ChainSafe/gossamer/lib/babe"
	"github.com/ChainSafe/gossamer/lib/common"
	"github.com/ChainSafe/gossamer/lib/crypto"
	"github.com/ChainSafe/gossamer/lib/crypto/ed25519"
	"github.com/ChainSafe/gossamer/lib/crypto/sr25519"
	"github.com/ChainSafe/gossamer/lib/grandpa"
	"github.com/ChainSafe/gossamer/lib/keystore"
	"github.com/ChainSafe/gossamer/lib/runtime"
	"github.com/ChainSafe/gossamer/lib/runtime/wasmer"
	"github.com/ChainSafe/gossamer/lib/utils"
)

// BlockProducer to produce blocks
type BlockProducer interface {
	Pause() error
	Resume() error
	EpochLength() uint64
	SlotDuration() uint64
}

type rpcServiceSettings struct {
	config        *Config
	nodeStorage   *runtime.NodeStorage
	state         *state.Service
	core          *core.Service
	network       *network.Service
	blockProducer BlockProducer
	system        *system.Service
	blockFinality *grandpa.Service
	syncer        *sync.Service
}

func newInMemoryDB() (*chaindb.BadgerDB, error) {
	return utils.SetupDatabase("", true)
}

// createStateService creates the state service and initialise state database
func (nodeBuilder) createStateService(cfg *Config) (*state.Service, error) {
	logger.Debug("creating state service...")

	config := state.Config{
		Path:     cfg.Global.BasePath,
		LogLevel: cfg.Log.StateLvl,
		Metrics:  metrics.NewIntervalConfig(cfg.Global.PublishMetrics),
	}

	stateSrvc := state.NewService(config)

	err := stateSrvc.SetupBase()
	if err != nil {
		return nil, fmt.Errorf("cannot setup base: %w", err)
	}

	return stateSrvc, nil
}

func startStateService(cfg *Config, stateSrvc *state.Service) error {
	logger.Debug("starting state service...")

	// start state service (initialise state database)
	err := stateSrvc.Start()
	if err != nil {
		return fmt.Errorf("failed to start state service: %w", err)
	}

	if cfg.State.Rewind != 0 {
		err = stateSrvc.Rewind(cfg.State.Rewind)
		if err != nil {
			return fmt.Errorf("failed to rewind state: %w", err)
		}
	}

	return nil
}

func (nodeBuilder) createRuntimeStorage(st *state.Service) (*runtime.NodeStorage, error) {
	localStorage, err := newInMemoryDB()
	if err != nil {
		return nil, err
	}

	return &runtime.NodeStorage{
		LocalStorage:      localStorage,
		PersistentStorage: chaindb.NewTable(st.DB(), "offlinestorage"),
		BaseDB:            st.Base,
	}, nil
}

func createRuntime(cfg *Config, ns runtime.NodeStorage, st *state.Service,
	ks *keystore.GlobalKeystore, net *network.Service, code []byte) (
	rt runtimeInterface, err error) {
	logger.Info("creating runtime with interpreter " + cfg.Core.WasmInterpreter + "...")

	// check if code substitute is in use, if so replace code
	codeSubHash := st.Base.LoadCodeSubstitutedBlockHash()

	if !codeSubHash.IsEmpty() {
		logger.Infof("🔄 detected runtime code substitution, upgrading to block hash %s...", codeSubHash)
		genData, err := st.Base.LoadGenesisData()
		if err != nil {
			return nil, err
		}
		codeString := genData.CodeSubstitutes[codeSubHash.String()]

		code = common.MustHexToBytes(codeString)
	}

	ts, err := st.Storage.TrieState(nil)
	if err != nil {
		return nil, err
	}

	codeHash, err := st.Storage.LoadCodeHash(nil)
	if err != nil {
		return nil, err
	}

	switch cfg.Core.WasmInterpreter {
	case wasmer.Name:
		rtCfg := wasmer.Config{
			Storage:     ts,
			Keystore:    ks,
			LogLvl:      cfg.Log.RuntimeLvl,
			NodeStorage: ns,
			Network:     net,
			Role:        cfg.Core.Roles,
			CodeHash:    codeHash,
		}

		// create runtime executor
		rt, err = wasmer.NewInstance(code, rtCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create runtime executor: %s", err)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrWasmInterpreterName, cfg.Core.WasmInterpreter)
	}

	st.Block.StoreRuntime(st.Block.BestBlockHash(), rt)
	return rt, nil
}

func asAuthority(authority bool) string {
	if authority {
		return " as authority"
	}
	return ""
}

// ServiceBuilder interface to define the building of babe service
type ServiceBuilder interface {
	NewServiceIFace(cfg *babe.ServiceConfig) (service *babe.Service, err error)
}

var _ ServiceBuilder = (*babe.Builder)(nil)

func (nb nodeBuilder) createBABEService(cfg *Config, st *state.Service, ks KeyStore,
	cs *core.Service, telemetryMailer Telemetry) (service *babe.Service, err error) {
	return nb.createBABEServiceWithBuilder(cfg, st, ks, cs, telemetryMailer, babe.Builder{})
}

// KeyStore is the keystore interface for the BABE service.
type KeyStore interface {
	Name() keystore.Name
	Type() string
	Keypairs() []keystore.KeyPair
}

func (nodeBuilder) createBABEServiceWithBuilder(cfg *Config, st *state.Service, ks KeyStore,
	cs *core.Service, telemetryMailer Telemetry, newBabeService ServiceBuilder) (
	service *babe.Service, err error) {
	logger.Info("creating BABE service" +
		asAuthority(cfg.Core.BabeAuthority) + "...")

	if ks.Name() != "babe" || ks.Type() != crypto.Sr25519Type {
		return nil, ErrInvalidKeystoreType
	}

	kps := ks.Keypairs()
	logger.Infof("keystore with keys %v", kps)
	if len(kps) == 0 && cfg.Core.BabeAuthority {
		return nil, ErrNoKeysProvided
	}

	bcfg := &babe.ServiceConfig{
		LogLvl:             cfg.Log.BlockProducerLvl,
		BlockState:         st.Block,
		StorageState:       st.Storage,
		TransactionState:   st.Transaction,
		EpochState:         st.Epoch,
		BlockImportHandler: cs,
		Authority:          cfg.Core.BabeAuthority,
		IsDev:              cfg.Global.ID == "dev",
		Lead:               cfg.Core.BABELead,
		Telemetry:          telemetryMailer,
	}

	if cfg.Core.BabeAuthority {
		bcfg.Keypair = kps[0].(*sr25519.Keypair)
	}

	bs, err := newBabeService.NewServiceIFace(bcfg)
	if err != nil {
		logger.Errorf("failed to initialise BABE service: %s", err)
		return nil, err
	}
	return bs, nil
}

// Core Service

// createCoreService creates the core service from the provided core configuration
func (nodeBuilder) createCoreService(cfg *Config, ks *keystore.GlobalKeystore,
	st *state.Service, net *network.Service, dh *digest.Handler) (
	*core.Service, error) {
	logger.Debug("creating core service" +
		asAuthority(cfg.Core.Roles == common.AuthorityRole) +
		"...")

	genesisData, err := st.Base.LoadGenesisData()
	if err != nil {
		return nil, err
	}

	codeSubs := make(map[common.Hash]string)
	for k, v := range genesisData.CodeSubstitutes {
		codeSubs[common.MustHexToHash(k)] = v
	}

	// set core configuration
	coreConfig := &core.Config{
		LogLvl:               cfg.Log.CoreLvl,
		BlockState:           st.Block,
		StorageState:         st.Storage,
		TransactionState:     st.Transaction,
		Keystore:             ks,
		Network:              net,
		CodeSubstitutes:      codeSubs,
		CodeSubstitutedState: st.Base,
	}

	// create new core service
	coreSrvc, err := core.NewService(coreConfig)
	if err != nil {
		logger.Errorf("failed to create core service: %s", err)
		return nil, err
	}

	return coreSrvc, nil
}

// Network Service

// createNetworkService creates a network service from the command configuration and genesis data
func (nodeBuilder) createNetworkService(cfg *Config, stateSrvc *state.Service,
	telemetryMailer Telemetry) (*network.Service, error) {
	logger.Debugf(
		"creating network service with roles %d, port %d, bootnodes %s, protocol ID %s, nobootstrap=%t and noMDNS=%t...",
		cfg.Core.Roles, cfg.Network.Port, strings.Join(cfg.Network.Bootnodes, ","), cfg.Network.ProtocolID,
		cfg.Network.NoBootstrap, cfg.Network.NoMDNS)

	slotDuration, err := stateSrvc.Epoch.GetSlotDuration()
	if err != nil {
		return nil, fmt.Errorf("cannot get slot duration: %w", err)
	}

	// network service configuation
	networkConfig := network.Config{
		LogLvl:            cfg.Log.NetworkLvl,
		BlockState:        stateSrvc.Block,
		BasePath:          cfg.Global.BasePath,
		Roles:             cfg.Core.Roles,
		Port:              cfg.Network.Port,
		Bootnodes:         cfg.Network.Bootnodes,
		ProtocolID:        cfg.Network.ProtocolID,
		NoBootstrap:       cfg.Network.NoBootstrap,
		NoMDNS:            cfg.Network.NoMDNS,
		MinPeers:          cfg.Network.MinPeers,
		MaxPeers:          cfg.Network.MaxPeers,
		PersistentPeers:   cfg.Network.PersistentPeers,
		DiscoveryInterval: cfg.Network.DiscoveryInterval,
		SlotDuration:      slotDuration,
		PublicIP:          cfg.Network.PublicIP,
		Telemetry:         telemetryMailer,
		PublicDNS:         cfg.Network.PublicDNS,
		Metrics:           metrics.NewIntervalConfig(cfg.Global.PublishMetrics),
	}

	networkSrvc, err := network.NewService(&networkConfig)
	if err != nil {
		logger.Errorf("failed to create network service: %s", err)
		return nil, err
	}

	return networkSrvc, nil
}

// RPC Service

// createRPCService creates the RPC service from the provided core configuration
func (nodeBuilder) createRPCService(params rpcServiceSettings) (*rpc.HTTPServer, error) {
	logger.Infof(
		"creating rpc service with host %s, external=%t, port %d, modules %s, ws=%t, ws port %d and ws external=%t",
		params.config.RPC.Host,
		params.config.RPC.External,
		params.config.RPC.Port,
		strings.Join(params.config.RPC.Modules, ","),
		params.config.RPC.WS,
		params.config.RPC.WSPort,
		params.config.RPC.WSExternal,
	)
	rpcService := rpc.NewService()

	genesisData, err := params.state.Base.LoadGenesisData()
	if err != nil {
		return nil, fmt.Errorf("failed to load genesis data: %s", err)
	}

	syncStateSrvc, err := modules.NewStateSync(genesisData, params.state.Storage)
	if err != nil {
		return nil, fmt.Errorf("failed to create sync state service: %s", err)
	}

	rpcConfig := &rpc.HTTPServerConfig{
		LogLvl:              params.config.Log.RPCLvl,
		BlockAPI:            params.state.Block,
		StorageAPI:          params.state.Storage,
		NetworkAPI:          params.network,
		CoreAPI:             params.core,
		NodeStorage:         params.nodeStorage,
		BlockProducerAPI:    params.blockProducer,
		BlockFinalityAPI:    params.blockFinality,
		TransactionQueueAPI: params.state.Transaction,
		RPCAPI:              rpcService,
		SyncStateAPI:        syncStateSrvc,
		SyncAPI:             params.syncer,
		SystemAPI:           params.system,
		RPC:                 params.config.RPC.Enabled,
		RPCExternal:         params.config.RPC.External,
		RPCUnsafe:           params.config.RPC.Unsafe,
		RPCUnsafeExternal:   params.config.RPC.UnsafeExternal,
		Host:                params.config.RPC.Host,
		RPCPort:             params.config.RPC.Port,
		WS:                  params.config.RPC.WS,
		WSExternal:          params.config.RPC.WSExternal,
		WSUnsafe:            params.config.RPC.WSUnsafe,
		WSUnsafeExternal:    params.config.RPC.WSUnsafeExternal,
		WSPort:              params.config.RPC.WSPort,
		Modules:             params.config.RPC.Modules,
	}

	return rpc.NewHTTPServer(rpcConfig), nil
}

// createSystemService creates a systemService for providing system related information
func (nodeBuilder) createSystemService(cfg *types.SystemInfo, stateSrvc *state.Service) (*system.Service, error) {
	genesisData, err := stateSrvc.Base.LoadGenesisData()
	if err != nil {
		return nil, err
	}

	return system.NewService(cfg, genesisData), nil
}

// createGRANDPAService creates a new GRANDPA service
func (nodeBuilder) createGRANDPAService(cfg *Config, st *state.Service, ks KeyStore,
	net *network.Service, telemetryMailer Telemetry) (*grandpa.Service, error) {
	bestBlockHash := st.Block.BestBlockHash()
	rt, err := st.Block.GetRuntime(bestBlockHash)
	if err != nil {
		return nil, err
	}

	ad, err := rt.GrandpaAuthorities()
	if err != nil {
		return nil, err
	}

	if ks.Name() != "gran" || ks.Type() != crypto.Ed25519Type {
		return nil, ErrInvalidKeystoreType
	}

	voters := types.NewGrandpaVotersFromAuthorities(ad)

	keys := ks.Keypairs()
	if len(keys) == 0 && cfg.Core.GrandpaAuthority {
		return nil, errors.New("no ed25519 keys provided for GRANDPA")
	}

	gsCfg := &grandpa.Config{
		LogLvl:       cfg.Log.FinalityGadgetLvl,
		BlockState:   st.Block,
		GrandpaState: st.Grandpa,
		Voters:       voters,
		Authority:    cfg.Core.GrandpaAuthority,
		Network:      net,
		Interval:     cfg.Core.GrandpaInterval,
		Telemetry:    telemetryMailer,
	}

	if cfg.Core.GrandpaAuthority {
		gsCfg.Keypair = keys[0].(*ed25519.Keypair)
	}

	return grandpa.NewService(gsCfg)
}

func (nodeBuilder) createBlockVerifier(st *state.Service) *babe.VerificationManager {
	return babe.NewVerificationManager(st.Block, st.Epoch)
}

func (nodeBuilder) newSyncService(cfg *Config, st *state.Service, fg BlockJustificationVerifier,
	verifier *babe.VerificationManager, cs *core.Service, net *network.Service, telemetryMailer Telemetry) (
	*sync.Service, error) {
	slotDuration, err := st.Epoch.GetSlotDuration()
	if err != nil {
		return nil, err
	}

	syncCfg := &sync.Config{
		LogLvl:             cfg.Log.SyncLvl,
		Network:            net,
		BlockState:         st.Block,
		StorageState:       st.Storage,
		TransactionState:   st.Transaction,
		FinalityGadget:     fg,
		BabeVerifier:       verifier,
		BlockImportHandler: cs,
		MinPeers:           cfg.Network.MinPeers,
		MaxPeers:           cfg.Network.MaxPeers,
		SlotDuration:       slotDuration,
		Telemetry:          telemetryMailer,
	}

	return sync.NewService(syncCfg)
}

func (nodeBuilder) createDigestHandler(lvl log.Level, st *state.Service) (*digest.Handler, error) {
	return digest.NewHandler(lvl, st.Block, st.Epoch, st.Grandpa)
}

func createPprofService(settings pprof.Settings) (service *pprof.Service) {
	pprofLogger := log.NewFromGlobal(log.AddContext("pkg", "pprof"))
	return pprof.NewService(settings, pprofLogger)
}
