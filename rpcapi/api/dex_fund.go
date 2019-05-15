package api

import (
	"github.com/pkg/errors"
	"github.com/vitelabs/go-vite/chain"
	"github.com/vitelabs/go-vite/common/types"
	"github.com/vitelabs/go-vite/log15"
	"github.com/vitelabs/go-vite/vite"
	"github.com/vitelabs/go-vite/vm/contracts/dex"
	"github.com/vitelabs/go-vite/vm_db"
	"math/big"
)

type DexFundApi struct {
	chain chain.Chain
	log   log15.Logger
}

func NewDexFundApi(vite *vite.Vite) *DexFundApi {
	return &DexFundApi{
		chain: vite.Chain(),
		log:   log15.New("module", "rpc_api/dexfund_api"),
	}
}

func (f DexFundApi) String() string {
	return "DexFundApi"
}

type AccountFundInfo struct {
	TokenInfo *RpcTokenInfo `json:"tokenInfo,omitempty"`
	Available string        `json:"available"`
	Locked    string        `json:"locked"`
}

func (f DexFundApi) GetAccountFundInfo(addr types.Address, tokenId *types.TokenTypeId) (map[types.TokenTypeId]*AccountFundInfo, error) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return nil, err
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return nil, err
	}
	dexFund, _ := dex.GetUserFundFromStorage(db, addr)
	fundInfo, err := dex.GetAccountFundInfo(dexFund, tokenId)
	if err != nil {
		return nil, err
	}

	accFundInfo := make(map[types.TokenTypeId]*AccountFundInfo, 0)
	for _, v := range fundInfo {
		tokenInfo, err := f.chain.GetTokenInfoById(v.Token)
		if err != nil {
			return nil, err
		}
		info := &AccountFundInfo{TokenInfo: RawTokenInfoToRpc(tokenInfo, v.Token)}
		a := "0"
		if v.Available != nil {
			a = v.Available.String()
		}
		info.Available = a

		l := "0"
		if v.Locked != nil {
			l = v.Locked.String()
		}
		info.Locked = l

		accFundInfo[v.Token] = info
	}
	return accFundInfo, nil
}

func (f DexFundApi) GetAccountFundInfoByStatus(addr types.Address, tokenId *types.TokenTypeId, status byte) (map[types.TokenTypeId]string, error) {
	if status != 0 && status != 1 && status != 2 {
		return nil, errors.New("args's status error, 1 for available, 2 for locked, 0 for total")
	}

	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return nil, err
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return nil, err
	}
	dexFund, _ := dex.GetUserFundFromStorage(db, addr)
	fundInfo, err := dex.GetAccountFundInfo(dexFund, tokenId)
	if err != nil {
		return nil, err
	}

	fundInfoMap := make(map[types.TokenTypeId]string, 0)
	for _, v := range fundInfo {
		amount := big.NewInt(0)
		if a := v.Available; a != nil {
			if status == 0 || status == 1 {
				amount.Add(amount, a)
			}
		}
		if l := v.Locked; l != nil {
			if status == 0 || status == 2 {
				amount.Add(amount, l)
			}
		}
		fundInfoMap[v.Token] = amount.String()
	}
	return fundInfoMap, nil
}

func (f DexFundApi) VerifyFundBalance() (*dex.FundVerifyRes, error) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return nil, err
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return nil, err
	}
	return dex.VerifyDexFundBalance(db), nil
}

func (f DexFundApi) GetOwner() (*types.Address, error) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return nil, err
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return nil, err
	}
	return dex.GetOwner(db)
}

func (f DexFundApi) GetTime() (int64, error) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return -1, err
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return -1, err
	}
	return dex.GetTimestampInt64(db), nil
}

func (f DexFundApi) GetMarketInfo(tradeToken, quoteToken types.TokenTypeId) (*dex.MarketInfo, bool) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return nil, false
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return nil, false
	}
	return dex.GetMarketInfo(db, tradeToken, quoteToken)
}

func (f DexFundApi) GetPledgeForVX(address types.Address) (string, error) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return "", err
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return "", err
	}
	return dex.GetPledgeForVx(db, address).String(), nil
}

func (f DexFundApi) GetPledgeForVip(address types.Address) (*dex.PledgeVip, bool) {
	prevHash, err := getPrevBlockHash(f.chain, types.AddressDexFund)
	if err != nil {
		return nil, false
	}
	db, err := vm_db.NewVmDb(f.chain, &types.AddressDexFund, &f.chain.GetLatestSnapshotBlock().Hash, prevHash)
	if err != nil {
		return nil, false
	}
	return dex.GetPledgeForVip(db, address)
}
