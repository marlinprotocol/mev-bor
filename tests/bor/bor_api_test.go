package bor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/stretchr/testify/assert"
)

func duplicateInArray(arr []common.Hash) bool {
	visited := make(map[common.Hash]bool, 0)
	for i := 0; i < len(arr); i++ {
		if visited[arr[i]] == true {
			return true
		} else {
			visited[arr[i]] = true
		}
	}

	return false
}

func areDifferentHashes(receipts []map[string]interface{}) bool {
	addresses := []common.Hash{}
	for i := 0; i < len(receipts); i++ {
		addresses = append(addresses, receipts[i]["transactionHash"].(common.Hash))
		if duplicateInArray(addresses) {
			return false
		}
	}

	return true
}

func TestGetTransactionReceiptsByBlock(t *testing.T) {
	t.Parallel()

	var (
		key1, _    = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr       = crypto.PubkeyToAddress(key1.PublicKey)
		stack, _   = node.New(&node.DefaultConfig)
		backend, _ = eth.New(stack, &ethconfig.Defaults)
		db         = backend.ChainDb()
		hash1      = common.BytesToHash([]byte("topic1"))
		hash2      = common.BytesToHash([]byte("topic2"))
		hash3      = common.BytesToHash([]byte("topic3"))
		hash4      = common.BytesToHash([]byte("topic4"))
		hash5      = common.BytesToHash([]byte("topic5"))
	)

	defer func() {
		if err := stack.Close(); err != nil {
			t.Error(err)
		}
	}()

	genesis := core.GenesisBlockForTesting(db, addr, big.NewInt(1000000))
	sprint := params.TestChainConfig.Bor.Sprint

	chain, receipts := core.GenerateChain(params.TestChainConfig, genesis, ethash.NewFaker(), db, 6, func(i int, gen *core.BlockGen) {
		switch i {

		case 1: // 1 normal transaction on block 2
			receipt := types.NewReceipt(nil, false, 0)
			receipt.Logs = []*types.Log{
				{
					Address: addr,
					Topics:  []common.Hash{hash1},
				},
			}
			gen.AddUncheckedReceipt(receipt)
			gen.AddUncheckedTx(types.NewTransaction(24, common.HexToAddress("0x24"), big.NewInt(24), 24, gen.BaseFee(), nil))

		case 2: // 2 normal transactions on block 3
			receipt := types.NewReceipt(nil, false, 0)
			receipt.Logs = []*types.Log{
				{
					Address: addr,
					Topics:  []common.Hash{hash2},
				},
			}
			gen.AddUncheckedReceipt(receipt)
			gen.AddUncheckedTx(types.NewTransaction(992, common.HexToAddress("0x992"), big.NewInt(992), 992, gen.BaseFee(), nil))

			receipt2 := types.NewReceipt(nil, false, 0)
			receipt2.Logs = []*types.Log{
				{
					Address: addr,
					Topics:  []common.Hash{hash3},
				},
			}
			gen.AddUncheckedReceipt(receipt2)
			gen.AddUncheckedTx(types.NewTransaction(993, common.HexToAddress("0x993"), big.NewInt(993), 993, gen.BaseFee(), nil))

		case 3: // 1 normal transaction, 1 state-sync transaction on block 4
			receipt := types.NewReceipt(nil, false, 0)
			receipt.Logs = []*types.Log{
				{
					Address: addr,
					Topics:  []common.Hash{hash4},
				},
			}
			gen.AddUncheckedReceipt(receipt)
			gen.AddUncheckedTx(types.NewTransaction(1000, common.HexToAddress("0x0"), big.NewInt(1000), 1000, gen.BaseFee(), nil))

			// state-sync transaction
			receipt2 := types.NewReceipt(nil, false, 0)
			receipt2.Logs = []*types.Log{
				{
					Address: addr,
					Topics:  []common.Hash{hash5},
				},
			}
			gen.AddUncheckedReceipt(receipt2)
			// not adding unchecked tx as it will be added as a state-sync tx later

		}
	})

	for i, block := range chain {
		// write the block to database
		rawdb.WriteBlock(db, block)
		rawdb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
		rawdb.WriteHeadBlockHash(db, block.Hash())

		blockBatch := db.NewBatch()

		if i%int(sprint-1) != 0 {
			// if it is not sprint start write all the transactions as normal transactions.
			rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), receipts[i])
		} else {
			// check for blocks with receipts. Since in state-sync block, we have 1 normal txn and 1 state-sync txn.
			if len(receipts[i]) > 0 {
				// We write receipts for the normal transaction.
				rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), receipts[i][:1])

				// write the state-sync receipts to database => receipts[i][1:] => receipts[i][1]
				// State sync logs don't have tx index, tx hash and other necessary fields, DeriveFieldsForBorLogs will fill those fields for websocket subscriptions
				// DeriveFieldsForBorLogs argurments:
				// 1. State-sync logs
				// 2. Block Hash
				// 3. Block Number
				// 4. Transactions in the block(except state-sync) i.e. 1 in our case
				// 5. AllLogs -(minus) StateSyncLogs ; since we only have state-sync tx, it will be 1
				types.DeriveFieldsForBorLogs(receipts[i][1].Logs, block.Hash(), block.NumberU64(), uint(1), uint(1))

				rawdb.WriteBorReceipt(blockBatch, block.Hash(), block.NumberU64(), &types.ReceiptForStorage{
					Status: types.ReceiptStatusSuccessful, // make receipt status successful
					Logs:   receipts[i][1].Logs,
				})

				rawdb.WriteBorTxLookupEntry(blockBatch, block.Hash(), block.NumberU64())

			}

		}

		if err := blockBatch.Write(); err != nil {
			t.Error("Failed to write block into disk", "err", err)
		}
	}

	publicBlockchainAPI := backend.PublicBlockChainAPI()

	// check 1 : zero transactions
	receiptsOut, err := publicBlockchainAPI.GetTransactionReceiptsByBlock(context.Background(), rpc.BlockNumberOrHashWithNumber(1))
	if err != nil {
		t.Error(err)
	}

	assert.Equal(t, 0, len(receiptsOut))

	// check 2 : one transactions ( normal )
	receiptsOut, err = publicBlockchainAPI.GetTransactionReceiptsByBlock(context.Background(), rpc.BlockNumberOrHashWithNumber(2))
	if err != nil {
		t.Error(err)
	}

	assert.Equal(t, 1, len(receiptsOut))
	assert.True(t, areDifferentHashes(receiptsOut))

	// check 3 : two transactions ( both normal )
	receiptsOut, err = publicBlockchainAPI.GetTransactionReceiptsByBlock(context.Background(), rpc.BlockNumberOrHashWithNumber(3))
	if err != nil {
		t.Error(err)
	}

	assert.Equal(t, 2, len(receiptsOut))
	assert.True(t, areDifferentHashes(receiptsOut))

	// check 4 : two transactions ( one normal + one state-sync)
	receiptsOut, err = publicBlockchainAPI.GetTransactionReceiptsByBlock(context.Background(), rpc.BlockNumberOrHashWithNumber(4))
	if err != nil {
		t.Error(err)
	}

	assert.Equal(t, 2, len(receiptsOut))
	assert.True(t, areDifferentHashes(receiptsOut))
}