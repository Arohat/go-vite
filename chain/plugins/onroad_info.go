package chain_plugins

import (
	"fmt"
	"github.com/go-errors/errors"
	"github.com/golang/protobuf/proto"
	"github.com/vitelabs/go-vite/chain/db"
	"github.com/vitelabs/go-vite/chain/flusher"
	"github.com/vitelabs/go-vite/common/db/xleveldb"
	"github.com/vitelabs/go-vite/common/db/xleveldb/util"
	"github.com/vitelabs/go-vite/common/helper"
	"github.com/vitelabs/go-vite/common/types"
	"github.com/vitelabs/go-vite/ledger"
	"github.com/vitelabs/go-vite/log15"
	"github.com/vitelabs/go-vite/vitepb"
	"math/big"
	"strconv"
	"sync"
)

var (
	oLog                 = log15.New("plugin", "onroad_info")
	updateInfoErr        = errors.New("conflict, fail to update onroad info")
	updateUnconfirmedErr = errors.New("unconfirmed cache inconsistent")
)

type OnRoadInfo struct {
	chain Chain

	unconfirmedCache map[types.Address]map[types.Hash]*ledger.AccountBlock
	store            *chain_db.Store
	mu               sync.RWMutex
}

func newOnRoadInfo(store *chain_db.Store, chain Chain) Plugin {
	or := &OnRoadInfo{
		chain: chain,

		store:            store,
		unconfirmedCache: make(map[types.Address]map[types.Hash]*ledger.AccountBlock),
	}
	return or
}

func (or *OnRoadInfo) SetStore(store *chain_db.Store) {
	or.store = store
}

func (or *OnRoadInfo) reBuildOnRoadInfo(flusher *chain_flusher.Flusher) error {
	addrOnRoadMap, err := or.chain.LoadAllOnRoad()
	if err != nil {
		return err
	}
	for addr, hashList := range addrOnRoadMap {
		addrInfoMap := make(map[types.TokenTypeId]*onroadMeta, 0)

		// aggregate the data
		for _, v := range hashList {
			block, err := or.chain.GetAccountBlockByHash(v)
			if err != nil {
				return err
			}
			if block == nil {
				return errors.New("can't find the onroad block by hash")
			}
			om, ok := addrInfoMap[block.TokenId]
			if !ok || om == nil {
				om = &onroadMeta{
					TotalAmount: *big.NewInt(0),
					Number:      0,
				}
			}
			om.TotalAmount.Add(&om.TotalAmount, block.Amount)
			om.Number++
			addrInfoMap[block.TokenId] = om
		}

		batch := or.store.NewBatch()
		for tkId, om := range addrInfoMap {
			key := CreateOnRoadInfoKey(&addr, &tkId)
			if err := or.writeMeta(batch, key, om); err != nil {
				return err
			}
		}
		or.store.WriteDirectly(batch)
		// flush to disk
		flusher.Flush()
	}

	return nil
}

func (or *OnRoadInfo) InsertSnapshotBlock(batch *leveldb.Batch, snapshotBlock *ledger.SnapshotBlock, confirmedBlocks []*ledger.AccountBlock) error {
	addrOnRoadMap := excludePairTrades(or.chain, confirmedBlocks)

	or.mu.Lock()
	defer or.mu.Unlock()

	if err := or.removeUnconfirmed(addrOnRoadMap); err != nil {
		oLog.Error(fmt.Sprintf("removeUnconfirmed err:%v, sb[%v %v]", err, snapshotBlock.Height, snapshotBlock.Hash), "method", "InsertSnapshotBlock")
		// TODO redo the plugin onroad_info
	}

	if err := or.flushWriteBySnapshotLine(batch, addrOnRoadMap); err != nil {
		oLog.Error(fmt.Sprintf("flushWriteBySnapshotLine err:%v, sb[%v %v]", err, snapshotBlock.Height, snapshotBlock.Hash), "method", "InsertSnapshotBlock")
		// TODO redo the plugin onroad_info
	}

	return nil
}

func (or *OnRoadInfo) DeleteSnapshotBlocks(batch *leveldb.Batch, chunks []*ledger.SnapshotChunk) error {
	if len(chunks) <= 0 {
		return nil
	}

	blocks := make([]*ledger.AccountBlock, 0)
	for _, v := range chunks {
		if len(v.AccountBlocks) <= 0 {
			continue
		}
		blocks = append(blocks, v.AccountBlocks...)
	}
	addrOnRoadMap := excludePairTrades(or.chain, blocks)

	or.mu.Lock()
	defer or.mu.Unlock()

	// clean unconfirmed cache
	or.unconfirmedCache = make(map[types.Address]map[types.Hash]*ledger.AccountBlock)

	// revert flush the db
	err := or.flushDeleteBySnapshotLine(batch, addrOnRoadMap)
	if err != nil {
		heightStr := ""
		for _, v := range chunks {
			if v != nil && v.SnapshotBlock != nil {
				heightStr += strconv.FormatUint(v.SnapshotBlock.Height, 10) + ","
			}
		}
		oLog.Error(fmt.Sprintf("flushDeleteBySnapshotLine err:%v, sb[%v]", err, heightStr), "method", "DeleteSnapshotBlocks")
		// TODO redo the plugin onroad_info
	}

	return nil
}

func (or *OnRoadInfo) InsertAccountBlock(batch *leveldb.Batch, block *ledger.AccountBlock) error {
	blocks := make([]*ledger.AccountBlock, 0)
	blocks = append(blocks, block)
	addrOnRoadMap := excludePairTrades(or.chain, blocks)

	or.mu.Lock()
	defer or.mu.Unlock()

	or.addUnconfirmed(addrOnRoadMap)
	return nil
}

func (or *OnRoadInfo) DeleteAccountBlocks(batch *leveldb.Batch, blocks []*ledger.AccountBlock) error {
	addrOnRoadMap := excludePairTrades(or.chain, blocks)

	or.mu.Lock()
	defer or.mu.Unlock()

	if err := or.removeUnconfirmed(addrOnRoadMap); err != nil {
		oLog.Error(fmt.Sprintf("removeUnconfirmed err:%v", err), "method", "DeleteAccountBlocks")
		// TODO redo the plugin onroad_info
	}
	return nil
}

func (or *OnRoadInfo) GetAccountInfo(addr *types.Address) (*ledger.AccountInfo, error) {
	if addr == nil {
		return nil, nil
	}

	or.mu.RLock()
	defer or.mu.RUnlock()

	omMap, err := or.readOnRoadInfo(addr)
	if err != nil {
		return nil, err
	}

	signOmMap, err := or.getUnconfirmed(*addr)
	if err != nil {
		return nil, err
	}
	for tkId, signOm := range signOmMap {
		om, ok := omMap[tkId]
		if !ok || om == nil {
			om = &onroadMeta{
				TotalAmount: *big.NewInt(0),
				Number:      0,
			}
		}
		num := new(big.Int).SetUint64(om.Number)
		diffNum := num.Add(num, &signOm.number)
		diffAmount := om.TotalAmount.Add(&om.TotalAmount, &signOm.amount)

		oLog.Info(fmt.Sprintf("add unconfirmed info addr=%v tk=%v result[%v %v]\n", addr, tkId, diffNum.String(), diffAmount.String()), "method", "GetAccountInfo")

		if diffAmount.Sign() < 0 || diffNum.Sign() < 0 || (diffAmount.Sign() > 0 && diffNum.Sign() == 0) {
			return nil, errors.New("conflict, fail to get onroad info")
		}
		if diffNum.Sign() == 0 {
			delete(omMap, tkId)
			continue
		}
		om.TotalAmount = *diffAmount
		om.Number = diffNum.Uint64()
		omMap[tkId] = om
	}

	onroadInfo := &ledger.AccountInfo{
		AccountAddress:      *addr,
		TotalNumber:         0,
		TokenBalanceInfoMap: make(map[types.TokenTypeId]*ledger.TokenBalanceInfo),
	}
	balanceMap := onroadInfo.TokenBalanceInfoMap
	for k, v := range omMap {
		balanceMap[k] = &ledger.TokenBalanceInfo{
			TotalAmount: v.TotalAmount,
			Number:      v.Number,
		}
		onroadInfo.TotalNumber += v.Number
	}
	return onroadInfo, nil
}

func (or *OnRoadInfo) getUnconfirmed(addr types.Address) (map[types.TokenTypeId]*signOnRoadMeta, error) {
	onRoadMap, ok := or.unconfirmedCache[addr]
	if !ok || onRoadMap == nil {
		return nil, nil
	}
	pendingMap := make([]*ledger.AccountBlock, 0)
	for _, v := range onRoadMap {
		if v == nil {
			continue
		}
		pendingMap = append(pendingMap, v)
	}
	return or.aggregateBlocks(pendingMap)
}

func (or *OnRoadInfo) addUnconfirmed(addrMap map[types.Address][]*ledger.AccountBlock) {
	for addr, blockList := range addrMap {
		if len(blockList) <= 0 {
			continue
		}
		onRoadMap, ok := or.unconfirmedCache[addr]
		if !ok || onRoadMap == nil {
			onRoadMap = make(map[types.Hash]*ledger.AccountBlock)
		}
		for _, block := range blockList {
			var hashKey types.Hash
			if block.IsSendBlock() {
				hashKey = block.Hash
			} else {
				hashKey = block.FromBlockHash
			}
			value, ok := onRoadMap[hashKey]
			if ok && value != nil && (value.IsSendBlock() != block.IsSendBlock()) {
				delete(onRoadMap, hashKey)
			} else {
				onRoadMap[hashKey] = block
			}
			or.unconfirmedCache[addr] = onRoadMap
		}
	}
}

func (or *OnRoadInfo) removeUnconfirmed(addrMap map[types.Address][]*ledger.AccountBlock) error {
	for addr, blockList := range addrMap {
		if len(blockList) <= 0 {
			continue
		}
		onRoadMap, ok := or.unconfirmedCache[addr]
		if !ok || onRoadMap == nil {
			continue
		}
		for _, block := range blockList {
			var hashKey types.Hash
			if block.IsSendBlock() {
				hashKey = block.Hash
			} else {
				hashKey = block.FromBlockHash
			}
			// nil, R, S
			value, ok := onRoadMap[hashKey]
			if block.IsReceiveBlock() {
				if ok {
					if value == nil || value.IsReceiveBlock() {
						delete(onRoadMap, hashKey)
					} else {
						oLog.Error("%v remove R onroad:%v", updateUnconfirmedErr, hashKey)
						onRoadMap[hashKey] = nil
					}
				} else {
					fromBlock, err := or.chain.GetAccountBlockByHash(block.FromBlockHash)
					if err != nil {
						oLog.Error("fail to GetAccountBlockByHash, onroad:%v", hashKey)
					}
					// fromBlock may be nil
					onRoadMap[hashKey] = fromBlock
				}
			} else {
				if ok {
					// value == nil <- (delete-R put nil) || value != nil && value.IsSendBlock || value != nil && value.IsReceiveBlock
					if value == nil || value.IsSendBlock() {
						delete(onRoadMap, hashKey)
					} else {
						oLog.Error("%v remove S onroad:%v", updateUnconfirmedErr, hashKey)
						onRoadMap[hashKey] = nil
					}
				} else {
					// Nil is a placeholder in the case s&r are all in unconfirmed
					onRoadMap[hashKey] = nil
				}
			}
		}
	}
	return nil
}

func (or *OnRoadInfo) flushWriteBySnapshotLine(batch *leveldb.Batch, confirmedBlocks map[types.Address][]*ledger.AccountBlock) error {
	for addr, pendingList := range confirmedBlocks {
		signOmMap, err := or.aggregateBlocks(pendingList)
		if err != nil {
			return err
		}

		for tkId, signOm := range signOmMap {
			key := CreateOnRoadInfoKey(&addr, &tkId)
			om, err := or.getMeta(key)
			if err != nil {
				return err
			}
			if om == nil {
				om = &onroadMeta{
					TotalAmount: *big.NewInt(0),
					Number:      0,
				}
			}
			num := new(big.Int).SetUint64(om.Number)
			diffNum := num.Add(num, &signOm.number)
			diffAmount := om.TotalAmount.Add(&om.TotalAmount, &signOm.amount)

			if diffAmount.Sign() < 0 || diffNum.Sign() < 0 || (diffAmount.Sign() > 0 && diffNum.Sign() == 0) {
				return fmt.Errorf("%v addr=%v tkId=%v diffAmount=%v diffNum=%v", updateInfoErr, addr, tkId, diffAmount, diffNum)
			}
			if diffNum.Sign() == 0 {
				or.deleteMeta(batch, key)
				continue
			}
			om.TotalAmount = *diffAmount
			om.Number = diffNum.Uint64()
			if err := or.writeMeta(batch, key, om); err != nil {
				return err
			}
		}
	}
	return nil
}

func (or *OnRoadInfo) flushDeleteBySnapshotLine(batch *leveldb.Batch, confirmedBlocks map[types.Address][]*ledger.AccountBlock) error {
	for addr, pendingList := range confirmedBlocks {
		signOmMap, err := or.aggregateBlocks(pendingList)
		if err != nil {
			return err
		}
		for tkId, signOm := range signOmMap {
			key := CreateOnRoadInfoKey(&addr, &tkId)
			om, err := or.getMeta(key)
			if err != nil {
				return err
			}
			if om == nil {
				om = &onroadMeta{
					TotalAmount: *big.NewInt(0),
					Number:      0,
				}
			}
			num := new(big.Int).SetUint64(om.Number)
			diffNum := num.Sub(num, &signOm.number)
			diffAmount := om.TotalAmount.Sub(&om.TotalAmount, &signOm.amount)
			if diffAmount.Sign() < 0 || diffNum.Sign() < 0 || (diffAmount.Sign() > 0 && diffNum.Sign() == 0) {
				return fmt.Errorf("%v addr=%v tkId=%v diffAmount=%v diffNum=%v", updateInfoErr, addr, tkId, diffAmount, diffNum)
			}
			if diffNum.Sign() == 0 {
				or.deleteMeta(batch, key)
				continue
			}
			om.TotalAmount = *diffAmount
			om.Number = diffNum.Uint64()
			if err := or.writeMeta(batch, key, om); err != nil {
				return err
			}
		}
	}
	return nil
}

func (or *OnRoadInfo) readOnRoadInfo(addr *types.Address) (map[types.TokenTypeId]*onroadMeta, error) {
	omMap := make(map[types.TokenTypeId]*onroadMeta)
	iter := or.store.NewIterator(util.BytesPrefix(CreateOnRoadInfoPrefixKey(addr)))
	defer iter.Release()

	for iter.Next() {
		key := iter.Key()
		tokenTypeIdBytes := key[1+types.AddressSize : 1+types.AddressSize+types.TokenTypeIdSize]
		tokenTypeId, err := types.BytesToTokenTypeId(tokenTypeIdBytes)
		if err != nil {
			return nil, err
		}
		om := &onroadMeta{}
		if err := om.deserialize(iter.Value()); err != nil {
			return nil, err
		}
		omMap[tokenTypeId] = om
	}
	if err := iter.Error(); err != nil && err != leveldb.ErrNotFound {
		return nil, err
	}
	return omMap, nil
}

func (or *OnRoadInfo) getMeta(key []byte) (*onroadMeta, error) {
	value, err := or.store.Get(key)
	if err != nil {
		return nil, err
	}
	if len(value) <= 0 {
		return nil, nil
	}
	om := &onroadMeta{}
	if err := om.deserialize(value); err != nil {
		return nil, err
	}
	return om, nil
}

func (or *OnRoadInfo) writeMeta(batch *leveldb.Batch, key []byte, meta *onroadMeta) error {
	dataSlice, sErr := meta.serialize()
	if sErr != nil {
		return sErr
	}
	batch.Put(key, dataSlice)
	return nil
}

func (or *OnRoadInfo) deleteMeta(batch *leveldb.Batch, key []byte) {
	batch.Delete(key)
}

type signOnRoadMeta struct {
	amount big.Int
	number big.Int
}

func (or *OnRoadInfo) aggregateBlocks(blocks []*ledger.AccountBlock) (map[types.TokenTypeId]*signOnRoadMeta, error) {
	addMap := make(map[types.TokenTypeId]*signOnRoadMeta)
	for _, block := range blocks {
		if block.IsSendBlock() {
			v, ok := addMap[block.TokenId]
			if !ok || v == nil {
				v = &signOnRoadMeta{
					amount: *big.NewInt(0),
					number: *big.NewInt(0),
				}
			}
			if block.Amount != nil {
				v.amount.Add(&v.amount, block.Amount)
			}
			v.number.Add(&v.number, helper.Big1)
			addMap[block.TokenId] = v
		} else {
			fromBlock, err := or.chain.GetAccountBlockByHash(block.FromBlockHash)
			if err != nil {
				return nil, err
			}
			if fromBlock == nil {
				return nil, errors.New("failed to find onroad by recv")
			}
			v, ok := addMap[fromBlock.TokenId]
			if !ok || v == nil {
				v = &signOnRoadMeta{
					amount: *big.NewInt(0),
					number: *big.NewInt(0),
				}
			}
			if fromBlock.Amount != nil {
				v.amount.Sub(&v.amount, fromBlock.Amount)
			}
			v.number.Sub(&v.number, helper.Big1)
			addMap[fromBlock.TokenId] = v
		}
	}
	return addMap, nil
}

type onroadMeta struct {
	TotalAmount big.Int
	Number      uint64
}

func (om *onroadMeta) proto() *vitepb.OnroadMeta {
	pb := &vitepb.OnroadMeta{}
	pb.Num = om.Number
	pb.Amount = om.TotalAmount.Bytes()
	return pb
}

func (om *onroadMeta) deProto(pb *vitepb.OnroadMeta) {
	om.Number = pb.Num
	totalAmount := big.NewInt(0)
	if len(pb.Amount) > 0 {
		totalAmount.SetBytes(pb.Amount)
	}
	om.TotalAmount = *totalAmount
}

func (om *onroadMeta) serialize() ([]byte, error) {
	return proto.Marshal(om.proto())
}

func (om *onroadMeta) deserialize(buf []byte) error {
	pb := &vitepb.OnroadMeta{}
	if err := proto.Unmarshal(buf, pb); err != nil {
		return err
	}
	om.deProto(pb)
	return nil
}

func excludePairTrades(chain Chain, blockList []*ledger.AccountBlock) map[types.Address][]*ledger.AccountBlock {
	cutMap := make(map[types.Hash]*ledger.AccountBlock)
	for _, block := range blockList {
		if block.IsSendBlock() {
			v, ok := cutMap[block.Hash]
			if ok && v != nil && v.IsReceiveBlock() {
				delete(cutMap, block.Hash)
			} else {
				cutMap[block.Hash] = block
			}
			continue
		}

		if chain.IsGenesisAccountBlock(block.Hash) {
			continue
		}

		// receive block
		v, ok := cutMap[block.FromBlockHash]
		if ok && v != nil && v.IsSendBlock() {
			delete(cutMap, block.FromBlockHash)
		} else {
			cutMap[block.FromBlockHash] = block
		}

		// sendBlockList
		if !types.IsContractAddr(block.AccountAddress) || len(block.SendBlockList) <= 0 {
			continue
		}
		for _, subSend := range block.SendBlockList {
			v, ok := cutMap[subSend.Hash]
			if ok && v != nil && v.IsReceiveBlock() {
				delete(cutMap, subSend.Hash)
			} else {
				cutMap[subSend.Hash] = subSend
			}
		}
	}

	pendingMap := make(map[types.Address][]*ledger.AccountBlock)
	for _, v := range cutMap {
		if v == nil {
			continue
		}
		var addr *types.Address
		if v.IsSendBlock() {
			addr = &v.ToAddress
		} else {
			addr = &v.AccountAddress
		}
		_, ok := pendingMap[*addr]
		if !ok {
			pendingMap[*addr] = make([]*ledger.AccountBlock, 0)
		}
		pendingMap[*addr] = append(pendingMap[*addr], v)
	}
	return pendingMap
}
