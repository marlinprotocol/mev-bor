// Copyright 2018 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"math/big"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"gotest.tools/assert"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/api"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/consensus/clique"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/tests/bor/mocks"
)

// TODO(raneet10): Duplicate initialization from miner/test_backend.go . Recheck whether we need both
// func init() {
// 	testTxPoolConfig = txpool.DefaultConfig
// 	testTxPoolConfig.Journal = ""
// 	ethashChainConfig = new(params.ChainConfig)
// 	*ethashChainConfig = *params.TestChainConfig
// 	cliqueChainConfig = new(params.ChainConfig)
// 	*cliqueChainConfig = *params.TestChainConfig
// 	cliqueChainConfig.Clique = &params.CliqueConfig{
// 		Period: 10,
// 		Epoch:  30000,
// 	}

// 	signer := types.LatestSigner(params.TestChainConfig)
// 	tx1 := types.MustSignNewTx(testBankKey, signer, &types.AccessListTx{
// 		ChainID:  params.TestChainConfig.ChainID,
// 		Nonce:    0,
// 		To:       &testUserAddress,
// 		Value:    big.NewInt(1000),
// 		Gas:      params.TxGas,
// 		GasPrice: big.NewInt(params.InitialBaseFee),
// 	})
// 	pendingTxs = append(pendingTxs, tx1)

// 	tx2 := types.MustSignNewTx(testBankKey, signer, &types.LegacyTx{
// 		Nonce:    1,
// 		To:       &testUserAddress,
// 		Value:    big.NewInt(1000),
// 		Gas:      params.TxGas,
// 		GasPrice: big.NewInt(params.InitialBaseFee),
// 	})
// 	newTxs = append(newTxs, tx2)
// }

// newTestWorker creates a new test worker with the given parameters.
// nolint:unparam
func newTestWorker(t TensingObject, chainConfig *params.ChainConfig, engine consensus.Engine, db ethdb.Database, blocks int, noempty bool, delay uint, opcodeDelay uint) (*worker, *testWorkerBackend, func()) {
	backend := newTestWorkerBackend(t, chainConfig, engine, db, blocks)
	backend.txPool.AddLocals(pendingTxs)

	var w *worker

	if delay != 0 || opcodeDelay != 0 {
		//nolint:staticcheck
		w = newWorkerWithDelay(testConfig, chainConfig, engine, backend, new(event.TypeMux), nil, false, delay, opcodeDelay, &flashbotsData{
			isFlashbots: false,
			queue:       nil,
		})
	} else {
		//nolint:staticcheck
		w = newWorker(testConfig, chainConfig, engine, backend, new(event.TypeMux), nil, false, &flashbotsData{
			isFlashbots: false,
			queue:       nil,
		})
	}

	w.setEtherbase(TestBankAddress)

	// enable empty blocks
	w.noempty.Store(noempty)

	return w, backend, w.close
}

// nolint : paralleltest
func TestGenerateBlockAndImportEthash(t *testing.T) {
	testGenerateBlockAndImport(t, false, false)
}

// nolint : paralleltest
func TestGenerateBlockAndImportClique(t *testing.T) {
	testGenerateBlockAndImport(t, true, false)
}

// nolint : paralleltest
func TestGenerateBlockAndImportBor(t *testing.T) {
	testGenerateBlockAndImport(t, false, true)
}

//nolint:thelper
func testGenerateBlockAndImport(t *testing.T, isClique bool, isBor bool) {
	var (
		engine      consensus.Engine
		chainConfig params.ChainConfig
		db          = rawdb.NewMemoryDatabase()
		ctrl        *gomock.Controller
	)

	if isBor {
		chainConfig = *params.BorUnittestChainConfig

		engine, ctrl = getFakeBorFromConfig(t, &chainConfig)
		defer ctrl.Finish()
	} else {
		if isClique {
			chainConfig = *params.AllCliqueProtocolChanges
			chainConfig.Clique = &params.CliqueConfig{Period: 1, Epoch: 30000}
			engine = clique.New(chainConfig.Clique, db)
		} else {
			chainConfig = *params.AllEthashProtocolChanges
			engine = ethash.NewFaker()
		}
	}

	defer engine.Close()

	w, b, _ := newTestWorker(t, &chainConfig, engine, db, 0, false, 0, 0)
	defer w.close()

	// This test chain imports the mined blocks.
	chain, _ := core.NewBlockChain(rawdb.NewMemoryDatabase(), nil, b.Genesis, nil, engine, vm.Config{}, nil, nil, nil)
	defer chain.Stop()

	// Ignore empty commit here for less noise.
	w.skipSealHook = func(task *task) bool {
		return len(task.receipts) == 0
	}

	// Wait for mined blocks.
	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	// Start mining!
	w.start()

	var (
		err   error
		uncle *types.Block
	)

	for i := 0; i < 5; i++ {
		err = b.txPool.AddLocal(b.newRandomTx(true))
		if err != nil {
			t.Fatal("while adding a local transaction", err)
		}

		err = b.txPool.AddLocal(b.newRandomTx(false))
		if err != nil {
			t.Fatal("while adding a remote transaction", err)
		}

		uncle, err = b.newRandomUncle()
		if err != nil {
			t.Fatal("while making an uncle block", err)
		}

		w.postSideBlock(core.ChainSideEvent{Block: uncle})

		uncle, err = b.newRandomUncle()
		if err != nil {
			t.Fatal("while making an uncle block", err)
		}

		w.postSideBlock(core.ChainSideEvent{Block: uncle})

		select {
		case ev := <-sub.Chan():
			block := ev.Data.(core.NewMinedBlockEvent).Block
			if _, err := chain.InsertChain([]*types.Block{block}); err != nil {
				t.Fatalf("failed to insert new mined block %d: %v", block.NumberU64(), err)
			}
		case <-time.After(3 * time.Second): // Worker needs 1s to include new changes.
			t.Fatalf("timeout")
		}
	}
}

func getFakeBorFromConfig(t *testing.T, chainConfig *params.ChainConfig) (consensus.Engine, *gomock.Controller) {
	t.Helper()

	ctrl := gomock.NewController(t)

	ethAPIMock := api.NewMockCaller(ctrl)
	ethAPIMock.EXPECT().Call(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	spanner := bor.NewMockSpanner(ctrl)
	spanner.EXPECT().GetCurrentValidatorsByHash(gomock.Any(), gomock.Any(), gomock.Any()).Return([]*valset.Validator{
		{
			ID:               0,
			Address:          TestBankAddress,
			VotingPower:      100,
			ProposerPriority: 0,
		},
	}, nil).AnyTimes()

	heimdallClientMock := mocks.NewMockIHeimdallClient(ctrl)
	heimdallClientMock.EXPECT().Close().Times(1)

	contractMock := bor.NewMockGenesisContract(ctrl)

	db, _, _ := NewDBForFakes(t)

	engine := NewFakeBor(t, db, chainConfig, ethAPIMock, spanner, heimdallClientMock, contractMock)

	return engine, ctrl
}

func TestEmptyWorkEthash(t *testing.T) {
	t.Skip()
	testEmptyWork(t, ethashChainConfig, ethash.NewFaker())
}
func TestEmptyWorkClique(t *testing.T) {
	t.Skip()
	testEmptyWork(t, cliqueChainConfig, clique.New(cliqueChainConfig.Clique, rawdb.NewMemoryDatabase()))
}

func testEmptyWork(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	defer engine.Close()

	w, _, _ := newTestWorker(t, chainConfig, engine, rawdb.NewMemoryDatabase(), 0, false, 0, 0)
	defer w.close()

	var (
		taskIndex int
		taskCh    = make(chan struct{}, 2)
	)

	checkEqual := func(t *testing.T, task *task, index int) {
		// The first empty work without any txs included
		receiptLen, balance := 0, big.NewInt(0)

		if index == 1 {
			// The second full work with 1 tx included
			receiptLen, balance = 1, big.NewInt(1000)
		}

		if len(task.receipts) != receiptLen {
			t.Fatalf("receipt number mismatch: have %d, want %d", len(task.receipts), receiptLen)
		}

		if task.state.GetBalance(testUserAddress).Cmp(balance) != 0 {
			t.Fatalf("account balance mismatch: have %d, want %d", task.state.GetBalance(testUserAddress), balance)
		}
	}

	w.newTaskHook = func(task *task) {
		if task.block.NumberU64() == 1 {
			checkEqual(t, task, taskIndex)
			taskIndex += 1
			taskCh <- struct{}{}
		}
	}

	w.skipSealHook = func(task *task) bool { return true }
	w.fullTaskHook = func() {
		time.Sleep(100 * time.Millisecond)
	}
	w.start() // Start mining!

	for i := 0; i < 2; i += 1 {
		select {
		case <-taskCh:
		case <-time.NewTimer(3 * time.Second).C:
			t.Error("new task timeout")
		}
	}
}

func TestStreamUncleBlock(t *testing.T) {
	ethash := ethash.NewFaker()
	defer ethash.Close()

	w, b, _ := newTestWorker(t, ethashChainConfig, ethash, rawdb.NewMemoryDatabase(), 1, false, 0, 0)
	defer w.close()

	var taskCh = make(chan struct{}, 3)

	taskIndex := 0
	w.newTaskHook = func(task *task) {
		if task.block.NumberU64() == 2 {
			// The first task is an empty task, the second
			// one has 1 pending tx, the third one has 1 tx
			// and 1 uncle.
			if taskIndex == 2 {
				have := task.block.Header().UncleHash
				want := types.CalcUncleHash([]*types.Header{b.uncleBlock.Header()})

				if have != want {
					t.Errorf("uncle hash mismatch: have %s, want %s", have.Hex(), want.Hex())
				}
			}
			taskCh <- struct{}{}

			taskIndex += 1
		}
	}
	w.skipSealHook = func(task *task) bool {
		return true
	}
	w.fullTaskHook = func() {
		time.Sleep(100 * time.Millisecond)
	}
	w.start()

	for i := 0; i < 2; i += 1 {
		select {
		case <-taskCh:
		case <-time.NewTimer(time.Second).C:
			t.Error("new task timeout")
		}
	}

	w.postSideBlock(core.ChainSideEvent{Block: b.uncleBlock})

	select {
	case <-taskCh:
	case <-time.NewTimer(time.Second).C:
		t.Error("new task timeout")
	}
}

func TestRegenerateMiningBlockEthash(t *testing.T) {
	testRegenerateMiningBlock(t, ethashChainConfig, ethash.NewFaker())
}

func TestRegenerateMiningBlockClique(t *testing.T) {
	testRegenerateMiningBlock(t, cliqueChainConfig, clique.New(cliqueChainConfig.Clique, rawdb.NewMemoryDatabase()))
}

func testRegenerateMiningBlock(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	defer engine.Close()

	w, b, _ := newTestWorker(t, chainConfig, engine, rawdb.NewMemoryDatabase(), 0, false, 0, 0)
	defer w.close()

	var taskCh = make(chan struct{}, 3)

	taskIndex := 0
	w.newTaskHook = func(task *task) {
		if task.block.NumberU64() == 1 {
			// The first task is an empty task, the second
			// one has 1 pending tx, the third one has 2 txs
			if taskIndex == 2 {
				receiptLen, balance := 2, big.NewInt(2000)
				if len(task.receipts) != receiptLen {
					t.Errorf("receipt number mismatch: have %d, want %d", len(task.receipts), receiptLen)
				}

				if task.state.GetBalance(testUserAddress).Cmp(balance) != 0 {
					t.Errorf("account balance mismatch: have %d, want %d", task.state.GetBalance(testUserAddress), balance)
				}
			}
			taskCh <- struct{}{}

			taskIndex += 1
		}
	}
	w.skipSealHook = func(task *task) bool {
		return true
	}
	w.fullTaskHook = func() {
		time.Sleep(100 * time.Millisecond)
	}

	w.start()
	// Ignore the first two works
	for i := 0; i < 2; i += 1 {
		select {
		case <-taskCh:
		case <-time.NewTimer(time.Second).C:
			t.Error("new task timeout")
		}
	}
	b.txPool.AddLocals(newTxs)
	time.Sleep(time.Second)

	select {
	case <-taskCh:
	case <-time.NewTimer(time.Second).C:
		t.Error("new task timeout")
	}
}

func TestAdjustIntervalEthash(t *testing.T) {
	// Skipping this test as recommit interval would remain constant
	t.Skip()
	testAdjustInterval(t, ethashChainConfig, ethash.NewFaker())
}

func TestAdjustIntervalClique(t *testing.T) {
	// Skipping this test as recommit interval would remain constant
	t.Skip()
	testAdjustInterval(t, cliqueChainConfig, clique.New(cliqueChainConfig.Clique, rawdb.NewMemoryDatabase()))
}

func testAdjustInterval(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	defer engine.Close()

	w, _, _ := newTestWorker(t, chainConfig, engine, rawdb.NewMemoryDatabase(), 0, false, 0, 0)
	defer w.close()

	w.skipSealHook = func(task *task) bool {
		return true
	}
	w.fullTaskHook = func() {
		time.Sleep(100 * time.Millisecond)
	}

	var (
		progress = make(chan struct{}, 10)
		result   = make([]float64, 0, 10)
		index    = 0
		start    atomic.Bool
	)

	w.resubmitHook = func(minInterval time.Duration, recommitInterval time.Duration) {
		// Short circuit if interval checking hasn't started.
		if !start.Load() {
			return
		}

		var wantMinInterval, wantRecommitInterval time.Duration

		switch index {
		case 0:
			wantMinInterval, wantRecommitInterval = 3*time.Second, 3*time.Second
		case 1:
			origin := float64(3 * time.Second.Nanoseconds())
			estimate := origin*(1-intervalAdjustRatio) + intervalAdjustRatio*(origin/0.8+intervalAdjustBias)
			wantMinInterval, wantRecommitInterval = 3*time.Second, time.Duration(estimate)*time.Nanosecond
		case 2:
			estimate := result[index-1]
			min := float64(3 * time.Second.Nanoseconds())
			estimate = estimate*(1-intervalAdjustRatio) + intervalAdjustRatio*(min-intervalAdjustBias)
			wantMinInterval, wantRecommitInterval = 3*time.Second, time.Duration(estimate)*time.Nanosecond
		case 3:
			wantMinInterval, wantRecommitInterval = time.Second, time.Second
		}

		// Check interval
		if minInterval != wantMinInterval {
			t.Errorf("resubmit min interval mismatch: have %v, want %v ", minInterval, wantMinInterval)
		}

		if recommitInterval != wantRecommitInterval {
			t.Errorf("resubmit interval mismatch: have %v, want %v", recommitInterval, wantRecommitInterval)
		}

		result = append(result, float64(recommitInterval.Nanoseconds()))
		index += 1
		progress <- struct{}{}
	}
	w.start()

	time.Sleep(time.Second) // Ensure two tasks have been submitted due to start opt
	start.Store(true)

	w.setRecommitInterval(3 * time.Second)
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}

	w.resubmitAdjustCh <- &intervalAdjust{inc: true, ratio: 0.8}
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}

	w.resubmitAdjustCh <- &intervalAdjust{inc: false}
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}

	w.setRecommitInterval(500 * time.Millisecond)
	select {
	case <-progress:
	case <-time.NewTimer(time.Second).C:
		t.Error("interval reset timeout")
	}
}

func TestGetSealingWorkEthash(t *testing.T) {
	testGetSealingWork(t, ethashChainConfig, ethash.NewFaker())
}

func TestGetSealingWorkClique(t *testing.T) {
	testGetSealingWork(t, cliqueChainConfig, clique.New(cliqueChainConfig.Clique, rawdb.NewMemoryDatabase()))
}

func TestGetSealingWorkPostMerge(t *testing.T) {
	local := new(params.ChainConfig)
	*local = *ethashChainConfig
	local.TerminalTotalDifficulty = big.NewInt(0)
	testGetSealingWork(t, local, ethash.NewFaker())
}

// nolint:gocognit
func testGetSealingWork(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	t.Helper()

	defer engine.Close()

	w, b, _ := newTestWorker(t, chainConfig, engine, rawdb.NewMemoryDatabase(), 0, false, 0, 0)
	defer w.close()

	w.setExtra([]byte{0x01, 0x02})
	w.postSideBlock(core.ChainSideEvent{Block: b.uncleBlock})

	w.skipSealHook = func(task *task) bool {
		return true
	}
	w.fullTaskHook = func() {
		time.Sleep(100 * time.Millisecond)
	}
	timestamp := uint64(time.Now().Unix())
	assertBlock := func(block *types.Block, number uint64, coinbase common.Address, random common.Hash) {
		if block.Time() != timestamp {
			// Sometime the timestamp will be mutated if the timestamp
			// is even smaller than parent block's. It's OK.
			t.Logf("Invalid timestamp, want %d, get %d", timestamp, block.Time())
		}

		if len(block.Uncles()) != 0 {
			t.Error("Unexpected uncle block")
		}

		_, isClique := engine.(*clique.Clique)
		if !isClique {
			if len(block.Extra()) != 2 {
				t.Error("Unexpected extra field")
			}

			if block.Coinbase() != coinbase {
				t.Errorf("Unexpected coinbase got %x want %x", block.Coinbase(), coinbase)
			}
		} else {
			if block.Coinbase() != (common.Address{}) {
				t.Error("Unexpected coinbase")
			}
		}

		if !isClique {
			if block.MixDigest() != random {
				t.Error("Unexpected mix digest")
			}
		}

		if block.Nonce() != 0 {
			t.Error("Unexpected block nonce")
		}

		if block.NumberU64() != number {
			t.Errorf("Mismatched block number, want %d got %d", number, block.NumberU64())
		}
	}

	var cases = []struct {
		parent       common.Hash
		coinbase     common.Address
		random       common.Hash
		expectNumber uint64
		expectErr    bool
	}{
		{
			b.chain.Genesis().Hash(),
			common.HexToAddress("0xdeadbeef"),
			common.HexToHash("0xcafebabe"),
			uint64(1),
			false,
		},
		{
			b.chain.CurrentBlock().Hash(),
			common.HexToAddress("0xdeadbeef"),
			common.HexToHash("0xcafebabe"),
			b.chain.CurrentBlock().Number.Uint64() + 1,
			false,
		},
		{
			b.chain.CurrentBlock().Hash(),
			common.Address{},
			common.HexToHash("0xcafebabe"),
			b.chain.CurrentBlock().Number.Uint64() + 1,
			false,
		},
		{
			b.chain.CurrentBlock().Hash(),
			common.Address{},
			common.Hash{},
			b.chain.CurrentBlock().Number.Uint64() + 1,
			false,
		},
		{
			common.HexToHash("0xdeadbeef"),
			common.HexToAddress("0xdeadbeef"),
			common.HexToHash("0xcafebabe"),
			0,
			true,
		},
	}

	// This API should work even when the automatic sealing is not enabled
	for _, c := range cases {
		block, _, err := w.getSealingBlock(c.parent, timestamp, c.coinbase, c.random, nil, false)
		if c.expectErr {
			if err == nil {
				t.Error("Expect error but get nil")
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error %v", err)
			}

			assertBlock(block, c.expectNumber, c.coinbase, c.random)
		}
	}

	// This API should work even when the automatic sealing is enabled
	w.start()

	for _, c := range cases {
		block, _, err := w.getSealingBlock(c.parent, timestamp, c.coinbase, c.random, nil, false)
		if c.expectErr {
			if err == nil {
				t.Error("Expect error but get nil")
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error %v", err)
			}

			assertBlock(block, c.expectNumber, c.coinbase, c.random)
		}
	}
}

// nolint : paralleltest
// TestCommitInterruptExperimentBor tests the commit interrupt experiment for bor consensus by inducing an artificial delay at transaction level.
func TestCommitInterruptExperimentBor(t *testing.T) {
	// with 1 sec block time and 200 millisec tx delay we should get 5 txs per block
	testCommitInterruptExperimentBor(t, 200, 5, 0)

	time.Sleep(2 * time.Second)

	// with 1 sec block time and 100 millisec tx delay we should get 10 txs per block
	testCommitInterruptExperimentBor(t, 100, 10, 0)
}

// nolint : paralleltest
// TestCommitInterruptExperimentBorContract tests the commit interrupt experiment for bor consensus by inducing an artificial delay at OPCODE level.
func TestCommitInterruptExperimentBorContract(t *testing.T) {
	// pre-calculated number of OPCODES = 123. 7*123=861 < 1000, 1 tx is possible but 2 tx per block will not be possible.
	testCommitInterruptExperimentBorContract(t, 0, 1, 7)
	time.Sleep(2 * time.Second)
	// pre-calculated number of OPCODES = 123. 2*123=246 < 1000, 4 tx is possible but 5 tx per block will not be possible. But 3 happen due to other overheads.
	testCommitInterruptExperimentBorContract(t, 0, 3, 2)
	time.Sleep(2 * time.Second)
	// pre-calculated number of OPCODES = 123. 3*123=369 < 1000, 2 tx is possible but 3 tx per block will not be possible.
	testCommitInterruptExperimentBorContract(t, 0, 2, 3)
}

// nolint : thelper
// testCommitInterruptExperimentBorContract is a helper function for testing the commit interrupt experiment for bor consensus.
func testCommitInterruptExperimentBorContract(t *testing.T, delay uint, txCount int, opcodeDelay uint) {
	var (
		engine      consensus.Engine
		chainConfig *params.ChainConfig
		db          = rawdb.NewMemoryDatabase()
		ctrl        *gomock.Controller
		txInTxpool  = 100
		txs         = make([]*types.Transaction, 0, txInTxpool)
	)

	chainConfig = params.BorUnittestChainConfig

	log.Root().SetHandler(log.LvlFilterHandler(log.LvlInfo, log.StreamHandler(os.Stderr, log.TerminalFormat(true))))

	engine, ctrl = getFakeBorFromConfig(t, chainConfig)

	w, b, _ := newTestWorker(t, chainConfig, engine, db, 0, true, delay, opcodeDelay)
	defer func() {
		w.close()
		engine.Close()
		db.Close()
		ctrl.Finish()
	}()

	// nonce 0 tx
	tx, addr := b.newStorageCreateContractTx()
	if err := b.TxPool().AddRemote(tx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(4 * time.Second)

	// nonce starts from 1 because we already have one tx
	initNonce := uint64(1)

	for i := 0; i < txInTxpool; i++ {
		tx := b.newStorageContractCallTx(addr, initNonce+uint64(i))
		txs = append(txs, tx)
	}

	b.TxPool().AddRemotes(txs)

	// Start mining!
	w.start()
	time.Sleep(5 * time.Second)
	w.stop()

	currentBlockNumber := w.current.header.Number.Uint64()
	assert.Check(t, txCount >= w.chain.GetBlockByNumber(currentBlockNumber-1).Transactions().Len())
	assert.Check(t, 0 < w.chain.GetBlockByNumber(currentBlockNumber-1).Transactions().Len()+1)
}

// nolint : thelper
// testCommitInterruptExperimentBor is a helper function for testing the commit interrupt experiment for bor consensus.
func testCommitInterruptExperimentBor(t *testing.T, delay uint, txCount int, opcodeDelay uint) {
	var (
		engine      consensus.Engine
		chainConfig *params.ChainConfig
		db          = rawdb.NewMemoryDatabase()
		ctrl        *gomock.Controller
		txInTxpool  = 100
		txs         = make([]*types.Transaction, 0, txInTxpool)
	)

	chainConfig = params.BorUnittestChainConfig

	log.Root().SetHandler(log.LvlFilterHandler(log.LvlInfo, log.StreamHandler(os.Stderr, log.TerminalFormat(true))))

	engine, ctrl = getFakeBorFromConfig(t, chainConfig)

	w, b, _ := newTestWorker(t, chainConfig, engine, db, 0, true, delay, opcodeDelay)
	defer func() {
		w.close()
		engine.Close()
		db.Close()
		ctrl.Finish()
	}()

	// nonce starts from 0 because have no txs yet
	initNonce := uint64(0)

	for i := 0; i < txInTxpool; i++ {
		tx := b.newRandomTxWithNonce(false, initNonce+uint64(i))
		txs = append(txs, tx)
	}

	b.TxPool().AddRemotes(txs)

	// Start mining!
	w.start()
	time.Sleep(5 * time.Second)
	w.stop()

	currentBlockNumber := w.current.header.Number.Uint64()
	assert.Check(t, txCount >= w.chain.GetBlockByNumber(currentBlockNumber-1).Transactions().Len())
	assert.Check(t, 0 < w.chain.GetBlockByNumber(currentBlockNumber-1).Transactions().Len())
}

func BenchmarkBorMining(b *testing.B) {
	chainConfig := params.BorUnittestChainConfig

	ctrl := gomock.NewController(b)
	defer ctrl.Finish()

	ethAPIMock := api.NewMockCaller(ctrl)
	ethAPIMock.EXPECT().Call(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	spanner := bor.NewMockSpanner(ctrl)
	spanner.EXPECT().GetCurrentValidatorsByHash(gomock.Any(), gomock.Any(), gomock.Any()).Return([]*valset.Validator{
		{
			ID:               0,
			Address:          TestBankAddress,
			VotingPower:      100,
			ProposerPriority: 0,
		},
	}, nil).AnyTimes()

	heimdallClientMock := mocks.NewMockIHeimdallClient(ctrl)
	heimdallClientMock.EXPECT().Close().Times(1)

	contractMock := bor.NewMockGenesisContract(ctrl)

	db, _, _ := NewDBForFakes(b)

	engine := NewFakeBor(b, db, chainConfig, ethAPIMock, spanner, heimdallClientMock, contractMock)
	defer engine.Close()

	chainConfig.LondonBlock = big.NewInt(0)

	w, back, _ := newTestWorker(b, chainConfig, engine, db, 0, false, 0, 0)
	defer w.close()

	chain, _ := core.NewBlockChain(rawdb.NewMemoryDatabase(), nil, back.Genesis, nil, engine, vm.Config{}, nil, nil, nil)
	defer chain.Stop()

	// fulfill tx pool
	const (
		totalGas    = testGas + params.TxGas
		totalBlocks = 10
	)

	var err error

	txInBlock := int(back.Genesis.GasLimit/totalGas) + 1

	// a bit risky
	for i := 0; i < 2*totalBlocks*txInBlock; i++ {
		err = back.txPool.AddLocal(back.newRandomTx(true))
		if err != nil {
			b.Fatal("while adding a local transaction", err)
		}

		err = back.txPool.AddLocal(back.newRandomTx(false))
		if err != nil {
			b.Fatal("while adding a remote transaction", err)
		}
	}

	// Wait for mined blocks.
	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	b.ResetTimer()

	prev := uint64(time.Now().Unix())

	// Start mining!
	w.start()

	blockPeriod, ok := back.Genesis.Config.Bor.Period["0"]
	if !ok {
		blockPeriod = 1
	}

	for i := 0; i < totalBlocks; i++ {
		select {
		case ev := <-sub.Chan():
			block := ev.Data.(core.NewMinedBlockEvent).Block

			if _, err := chain.InsertChain([]*types.Block{block}); err != nil {
				b.Fatalf("failed to insert new mined block %d: %v", block.NumberU64(), err)
			}

			b.Log("block", block.NumberU64(), "time", block.Time()-prev, "txs", block.Transactions().Len(), "gasUsed", block.GasUsed(), "gasLimit", block.GasLimit())

			prev = block.Time()
		case <-time.After(time.Duration(blockPeriod) * time.Second):
			b.Fatalf("timeout")
		}
	}
}

// uses core.NewParallelBlockChain to use the dependencies present in the block header
// params.BorUnittestChainConfig contains the ParallelUniverseBlock ad big.NewInt(5), so the first 4 blocks will not have metadata.
// nolint: gocognit
func BenchmarkBorMiningBlockSTMMetadata(b *testing.B) {
	chainConfig := params.BorUnittestChainConfig

	ctrl := gomock.NewController(b)
	defer ctrl.Finish()

	ethAPIMock := api.NewMockCaller(ctrl)
	ethAPIMock.EXPECT().Call(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	spanner := bor.NewMockSpanner(ctrl)
	spanner.EXPECT().GetCurrentValidatorsByHash(gomock.Any(), gomock.Any(), gomock.Any()).Return([]*valset.Validator{
		{
			ID:               0,
			Address:          TestBankAddress,
			VotingPower:      100,
			ProposerPriority: 0,
		},
	}, nil).AnyTimes()

	heimdallClientMock := mocks.NewMockIHeimdallClient(ctrl)
	heimdallClientMock.EXPECT().Close().Times(1)

	contractMock := bor.NewMockGenesisContract(ctrl)

	db, _, _ := NewDBForFakes(b)

	engine := NewFakeBor(b, db, chainConfig, ethAPIMock, spanner, heimdallClientMock, contractMock)
	defer engine.Close()

	chainConfig.LondonBlock = big.NewInt(0)

	w, back, _ := NewTestWorker(b, chainConfig, engine, db, 0, false, 0, 0)
	defer w.close()

	// This test chain imports the mined blocks.
	db2 := rawdb.NewMemoryDatabase()
	back.Genesis.MustCommit(db2)

	chain, _ := core.NewParallelBlockChain(db2, nil, back.Genesis, nil, engine, vm.Config{ParallelEnable: true, ParallelSpeculativeProcesses: 8}, nil, nil, nil)
	defer chain.Stop()

	// Ignore empty commit here for less noise.
	w.skipSealHook = func(task *task) bool {
		return len(task.receipts) == 0
	}

	// fulfill tx pool
	const (
		totalGas    = testGas + params.TxGas
		totalBlocks = 10
	)

	var err error

	txInBlock := int(back.Genesis.GasLimit/totalGas) + 1

	// a bit risky
	for i := 0; i < 2*totalBlocks*txInBlock; i++ {
		err = back.txPool.AddLocal(back.newRandomTx(true))
		if err != nil {
			b.Fatal("while adding a local transaction", err)
		}

		err = back.txPool.AddLocal(back.newRandomTx(false))
		if err != nil {
			b.Fatal("while adding a remote transaction", err)
		}
	}

	// Wait for mined blocks.
	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	b.ResetTimer()

	prev := uint64(time.Now().Unix())

	// Start mining!
	w.start()

	blockPeriod, ok := back.Genesis.Config.Bor.Period["0"]
	if !ok {
		blockPeriod = 1
	}

	for i := 0; i < totalBlocks; i++ {
		select {
		case ev := <-sub.Chan():
			block := ev.Data.(core.NewMinedBlockEvent).Block

			if _, err := chain.InsertChain([]*types.Block{block}); err != nil {
				b.Fatalf("failed to insert new mined block %d: %v", block.NumberU64(), err)
			}

			// check for dependencies for block number > 4
			if block.NumberU64() <= 4 {
				if block.GetTxDependency() != nil {
					b.Fatalf("dependency not nil")
				}
			} else {
				deps := block.GetTxDependency()
				if len(deps[0]) != 0 {
					b.Fatalf("wrong dependency")
				}

				for i := 1; i < block.Transactions().Len(); i++ {
					if deps[i][0] != uint64(i-1) || len(deps[i]) != 1 {
						b.Fatalf("wrong dependency")
					}
				}
			}

			b.Log("block", block.NumberU64(), "time", block.Time()-prev, "txs", block.Transactions().Len(), "gasUsed", block.GasUsed(), "gasLimit", block.GasLimit())

			prev = block.Time()
		case <-time.After(time.Duration(blockPeriod) * time.Second):
			b.Fatalf("timeout")
		}
	}
}
