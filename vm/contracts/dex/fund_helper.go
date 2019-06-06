package dex

import (
	"fmt"
	"github.com/vitelabs/go-vite/common/types"
	"github.com/vitelabs/go-vite/ledger"
	"github.com/vitelabs/go-vite/vm/contracts/abi"
	cabi "github.com/vitelabs/go-vite/vm/contracts/abi"
	dexproto "github.com/vitelabs/go-vite/vm/contracts/dex/proto"
	"github.com/vitelabs/go-vite/vm/util"
	"github.com/vitelabs/go-vite/vm_db"
	"math/big"
	"strconv"
	"strings"
)

func CheckMarketParam(marketParam *ParamDexFundNewMarket, feeTokenId types.TokenTypeId, feeAmount *big.Int) (err error) {
	if feeTokenId != ledger.ViteTokenId {
		return fmt.Errorf("token type of fee for create market not valid")
	}
	if feeAmount.Cmp(NewMarketFeeAmount) < 0 {
		return fmt.Errorf("fee for create market not enough")
	}
	if marketParam.TradeToken == marketParam.QuoteToken {
		return TradeMarketInvalidTokenPairErr
	}

	return nil
}

func RenderMarketInfo(db vm_db.VmDb, marketInfo *MarketInfo, newMarketEvent *NewMarketEvent, tradeToken, quoteToken types.TokenTypeId, tradeTokenInfo *TokenInfo, address *types.Address) {
	quoteTokenInfo := QuoteTokenInfos[quoteToken]
	marketInfo.MarketSymbol = getDexTokenSymbol(quoteTokenInfo)
	marketInfo.TradeToken = tradeToken.Bytes()
	marketInfo.QuoteToken = quoteToken.Bytes()
	marketInfo.QuoteTokenDecimals = quoteTokenInfo.Decimals

	if _, ok := GetTokenInfo(db, marketParam.QuoteToken]; !ok {
		return TradeMarketInvalidQuoteTokenErr
	}
	if marketParam.QuoteToken == bitcoinToken && marketParam.TradeToken == usdtToken ||
		marketParam.QuoteToken == ethToken && (marketParam.TradeToken == usdtToken || marketParam.TradeToken == bitcoinToken) ||
		marketParam.QuoteToken == ledger.ViteTokenId && (marketParam.TradeToken == usdtToken || marketParam.TradeToken == bitcoinToken || marketParam.TradeToken == ethToken) {
		return TradeMarketInvalidTokenPairErr
	}


	var tradeTokenExists bool
	if tradeTokenInfo == nil {
		tradeTokenInfo, tradeTokenExists = GetTokenInfo(db, tradeToken)
	} else {
		tradeTokenExists = true
	}
	if tradeTokenExists {
		renderMarketInfoWithTradeTokenInfo(db, marketInfo, tradeTokenInfo)
		renderNewMarketEvent(marketInfo, newMarketEvent, tradeToken, quoteToken, tradeTokenInfo, quoteTokenInfo)
	}

	if marketInfo.Creator == nil {
		marketInfo.Creator = address.Bytes()
	}
	if marketInfo.Timestamp == 0 {
		marketInfo.Timestamp = GetTimestampInt64(db)
	}
}

func renderMarketInfoWithTradeTokenInfo(db vm_db.VmDb, marketInfo *MarketInfo, tradeTokenInfo *TokenInfo) {
	marketInfo.MarketId = NewAndSaveMarketId(db)
	marketInfo.MarketSymbol = fmt.Sprintf("%s_%s", getDexTokenSymbol(tradeTokenInfo), marketInfo.MarketSymbol)
	marketInfo.TradeTokenDecimals = tradeTokenInfo.Decimals
	marketInfo.Valid = true
	marketInfo.Owner = tradeTokenInfo.Owner
}

func renderNewMarketEvent(marketInfo *MarketInfo, newMarketEvent *NewMarketEvent, tradeToken, quoteToken types.TokenTypeId, tradeTokenInfo, quoteTokenInfo *TokenInfo) {
	newMarketEvent.MarketId = marketInfo.MarketId
	newMarketEvent.MarketSymbol = marketInfo.MarketSymbol
	newMarketEvent.TradeToken = tradeToken.Bytes()
	newMarketEvent.TradeTokenDecimals = tradeTokenInfo.Decimals
	newMarketEvent.DexTradeTokenSymbol = getDexTokenSymbol(tradeTokenInfo)
	newMarketEvent.QuoteToken = quoteToken.Bytes()
	newMarketEvent.QuoteTokenDecimals = quoteTokenInfo.Decimals
	newMarketEvent.DexQuoteTokenSymbol = getDexTokenSymbol(quoteTokenInfo)
	newMarketEvent.Creator = marketInfo.Creator
}

func OnNewMarketValid(db vm_db.VmDb, reader util.ConsensusReader, marketInfo *MarketInfo, newMarketEvent *NewMarketEvent, tradeToken, quoteToken types.TokenTypeId, address *types.Address) (block *ledger.AccountBlock) {
	userFee := &dexproto.UserFeeSettle{}
	userFee.Address = address.Bytes()
	userFee.BaseFee = NewMarketFeeDividendAmount.Bytes()
	inviteRelations := SettleFeeSumWithTokenId(db, reader, true, ledger.ViteTokenId, []*dexproto.UserFeeSettle{userFee}, NewMarketFeeDonateAmount, nil)
	SettleUserFees(db, reader, ledger.ViteTokenId.Bytes(), userFee, inviteRelations)
	SaveMarketInfo(db, marketInfo, tradeToken, quoteToken)
	AddNewMarketEventLog(db, newMarketEvent)
	var marketBytes, blockData []byte
	var err error
	if marketBytes, err = marketInfo.Serialize(); err != nil {
		panic(err)
	} else {
		if blockData, err = cabi.ABIDexTrade.PackMethod(cabi.MethodNameDexTradeNotifyNewMarket, marketBytes); err != nil {
			panic(err)
		} else {
			return &ledger.AccountBlock{
				AccountAddress: types.AddressDexFund,
				ToAddress:      types.AddressDexTrade,
				BlockType:      ledger.BlockTypeSendCall,
				TokenId:        ledger.ViteTokenId,
				Amount:         big.NewInt(0),
				Data:           blockData,
			}
		}
	}
}

func OnNewMarketPending(db vm_db.VmDb, param *ParamDexFundNewMarket, marketInfo *MarketInfo) []byte {
	SaveMarketInfo(db, marketInfo, param.TradeToken, param.QuoteToken)
	AddToPendingNewMarkets(db, param.TradeToken, param.QuoteToken)
	AddPendingNewMarketFeeSum(db)
	if data, err := abi.ABIMintage.PackMethod(abi.MethodNameGetTokenInfo, param.TradeToken, uint8(GetTokenForNewMarket)); err != nil {
		panic(err)
	} else {
		return data
	}
}

func OnNewMarketGetTokenInfoSuccess(db vm_db.VmDb, reader util.ConsensusReader, tradeTokenId types.TokenTypeId, tokenInfoRes *ParamDexFundGetTokenInfoCallback) (appendBlocks []*ledger.AccountBlock, err error) {
	tradeTokenInfo := newTokenInfoFromCallback(tokenInfoRes)
	SaveTokenInfo(db, tradeTokenId, tradeTokenInfo)
	var quoteTokens [][]byte
	if quoteTokens, err = FilterPendingNewMarkets(db, tradeTokenId); err != nil {
		return
	} else {
		for _, qt := range quoteTokens {
			var (
				quoteTokenId types.TokenTypeId
				marketInfo   *MarketInfo
				ok           bool
			)
			if quoteTokenId, err = types.BytesToTokenTypeId(qt); err != nil {
				panic(err)
			} else {
				if marketInfo, ok = GetMarketInfo(db, tradeTokenId, quoteTokenId); !ok || marketInfo.Valid {
					continue
				} else {
					var address types.Address
					if address, err = types.BytesToAddress(marketInfo.Creator); err != nil {
						panic(err)
					}
					newMarketEvent := &NewMarketEvent{}
					RenderMarketInfo(db, marketInfo, newMarketEvent, tradeTokenId, quoteTokenId, tradeTokenInfo, nil)
					SubPendingNewMarketFeeSum(db)
					appendBlocks = append(appendBlocks, OnNewMarketValid(db, reader, marketInfo, newMarketEvent, tradeTokenId, quoteTokenId, &address))
				}
			}
		}
		return
	}
}

func OnNewMarketGetTokenInfoFailed(db vm_db.VmDb, tradeTokenId types.TokenTypeId) (refundBlocks []*ledger.AccountBlock, err error) {
	var quoteTokens [][]byte
	if quoteTokens, err = FilterPendingNewMarkets(db, tradeTokenId); err != nil {
		return nil, err
	} else {
		refundBlocks = make([]*ledger.AccountBlock, len(quoteTokens))
		for index, qt := range quoteTokens {
			if quoteTokenId, err := types.BytesToTokenTypeId(qt); err != nil {
				panic(err)
			} else {
				if marketInfo, ok := GetMarketInfo(db, tradeTokenId, quoteTokenId); !ok || marketInfo.Valid {
					continue
				} else {
					SubPendingNewMarketFeeSum(db)
					DeleteMarketInfo(db, tradeTokenId, quoteTokenId)
					if address, err := types.BytesToAddress(marketInfo.Creator); err != nil {
						panic(err)
					} else {
						refundBlocks[index] = &ledger.AccountBlock{
							AccountAddress: types.AddressDexFund,
							ToAddress:      address,
							BlockType:      ledger.BlockTypeSendCall,
							Amount:         NewMarketFeeAmount,
							TokenId:        ledger.ViteTokenId,
						}
					}
				}
			}
		}
		return
	}
}

func OnSetQuoteGetTokenInfoSuccess(db vm_db.VmDb, tokenInfoRes *ParamDexFundGetTokenInfoCallback) error {
	if action, err := FilterPendingSetQuotes(db, tokenInfoRes.TokenId); err != nil {
		return err
	} else {
		tokenInfo := newTokenInfoFromCallback(tokenInfoRes)
		tokenInfo.QuoteType = int32(action.QuoteType)
		SaveTokenInfo(db, tokenInfoRes.TokenId, tokenInfo)
		return nil
	}
}

func OnSetQuoteGetTokenInfoFailed(db vm_db.VmDb, tradeTokenId types.TokenTypeId) (err error) {
	_, err = FilterPendingSetQuotes(db, tradeTokenId)
	return
}

func PreCheckOrderParam(orderParam *ParamDexFundNewOrder) error {
	if orderParam.Quantity.Sign() <= 0 {
		return InvalidOrderQuantityErr
	}
	// TODO add market order support
	if orderParam.OrderType != Limited {
		return InvalidOrderTypeErr
	}
	if orderParam.OrderType == Limited {
		if !ValidPrice(orderParam.Price) {
			return InvalidOrderPriceErr
		}
	}
	return nil
}

func RenderOrder(order *Order, param *ParamDexFundNewOrder, db vm_db.VmDb, address types.Address) (*MarketInfo, error) {
	var (
		marketInfo *MarketInfo
		ok         bool
	)
	if marketInfo, ok = GetMarketInfo(db, param.TradeToken, param.QuoteToken); !ok || !marketInfo.Valid {
		return nil, TradeMarketNotExistsErr
	} else if marketInfo.Stopped {
		return nil, TradeMarketStoppedErr
	}
	order.Id = ComposeOrderId(db, marketInfo.MarketId, param.Side, param.Price)
	order.MarketId = marketInfo.MarketId
	order.Address = address.Bytes()
	order.Side = param.Side
	order.Type = int32(param.OrderType)
	order.Price = PriceToBytes(param.Price)
	order.Quantity = param.Quantity.Bytes()
	RenderFeeRate(address, order, marketInfo, db)
	if order.Type == Limited {
		order.Amount = CalculateRawAmount(order.Quantity, order.Price, marketInfo.TradeTokenDecimals-marketInfo.QuoteTokenDecimals)
		if !order.Side { //buy
			order.LockedBuyFee = CalculateAmountForRate(order.Amount, MaxTotalFeeRate(*order))
		}
		totalAmount := AddBigInt(order.Amount, order.LockedBuyFee)
		if new(big.Int).SetBytes(totalAmount).Cmp(QuoteTokenMinAmount[param.QuoteToken]) < 0 {
			return marketInfo, OrderAmountTooSmallErr
		}
	}
	order.Status = Pending
	order.ExecutedQuantity = big.NewInt(0).Bytes()
	order.ExecutedAmount = big.NewInt(0).Bytes()
	order.RefundToken = []byte{}
	order.RefundQuantity = big.NewInt(0).Bytes()
	order.Timestamp = GetTimestampInt64(db)
	return marketInfo, nil
}

func RenderFeeRate(address types.Address, order *Order, marketInfo *MarketInfo, db vm_db.VmDb) {
	var vipReduceFeeRate int32 = 0
	if _, ok := GetPledgeForVip(db, address); ok {
		vipReduceFeeRate = VipReduceFeeRate
	}
	order.TakerFeeRate = BaseFeeRate - vipReduceFeeRate
	order.TakerBrokerFeeRate = marketInfo.TakerBrokerFeeRate
	order.MakerFeeRate = BaseFeeRate - vipReduceFeeRate
	order.MakerBrokerFeeRate = marketInfo.MakerBrokerFeeRate
	if _, err := GetInviterByInvitee(db, address); err == nil { // invited
		order.TakerFeeRate = order.TakerFeeRate * 9 / 10
		order.TakerBrokerFeeRate = order.TakerBrokerFeeRate * 9 / 10
		order.MakerFeeRate = order.MakerFeeRate * 9 / 10
		order.MakerBrokerFeeRate = order.MakerBrokerFeeRate * 9 / 10
	}
}

func CheckSettleActions(actions *dexproto.SettleActions) error {
	if actions == nil || len(actions.FundActions) == 0 && len(actions.FeeActions) == 0 {
		return fmt.Errorf("settle actions is emtpy")
	}
	for _, fund := range actions.FundActions {
		if len(fund.Address) != types.AddressSize {
			return fmt.Errorf("invalid address format for settle")
		}
		if len(fund.FundSettles) == 0 {
			return fmt.Errorf("no user funds to settle")
		}
	}
	return nil
}

func DepositAccount(db vm_db.VmDb, address types.Address, tokenId types.TokenTypeId, amount *big.Int) (*dexproto.Account) {
	dexFund, _ := GetUserFund(db, address)
	account, exists := GetAccountByTokeIdFromFund(dexFund, tokenId)
	available := new(big.Int).SetBytes(account.Available)
	account.Available = available.Add(available, amount).Bytes()
	if !exists {
		dexFund.Accounts = append(dexFund.Accounts, account)
	}
	SaveUserFund(db, address, dexFund)
	return account
}

func CheckAndLockFundForNewOrder(dexFund *UserFund, order *Order, marketInfo *MarketInfo) (err error) {
	var (
		lockToken, lockAmount []byte
		lockTokenId           types.TokenTypeId
		account               *dexproto.Account
		exists                bool
	)
	switch order.Side {
	case false: //buy
		lockToken = marketInfo.QuoteToken
		if order.Type == Limited {
			lockAmount = AddBigInt(order.Amount, order.LockedBuyFee)
		}
	case true: // sell
		lockToken = marketInfo.TradeToken
		lockAmount = order.Quantity
	}
	if tkId, err := types.BytesToTokenTypeId(lockToken); err != nil {
		panic(err)
	} else {
		lockTokenId = tkId
	}
	if account, exists = GetAccountByTokeIdFromFund(dexFund, lockTokenId); !exists {
		return ExceedFundAvailableErr
	}
	available := new(big.Int).SetBytes(account.Available)
	lockAmountToInc := new(big.Int).SetBytes(lockAmount)
	available = available.Sub(available, lockAmountToInc)
	if available.Sign() < 0 {
		return ExceedFundAvailableErr
	}
	account.Available = available.Bytes()
	account.Locked = AddBigInt(account.Locked, lockAmountToInc.Bytes())
	return
}

func HandlePledgeAction(db vm_db.VmDb, block *ledger.AccountBlock, pledgeType uint8, actionType int8, address types.Address, amount *big.Int) ([]*ledger.AccountBlock, error) {
	var (
		methodData []byte
		err        error
	)
	if actionType == Pledge {
		if methodData, err = pledgeRequest(db, address, pledgeType, amount); err != nil {
			return []*ledger.AccountBlock{}, err
		} else {
			return []*ledger.AccountBlock{
				{
					AccountAddress: block.AccountAddress,
					ToAddress:      types.AddressPledge,
					BlockType:      ledger.BlockTypeSendCall,
					Amount:         amount,
					TokenId:        ledger.ViteTokenId,
					Data:           methodData,
				},
			}, nil
		}
	} else {
		return DoCancelPledge(db, block, address, pledgeType, amount)
	}
}

func DoCancelPledge(db vm_db.VmDb, block *ledger.AccountBlock, address types.Address, pledgeType uint8, amount *big.Int) ([]*ledger.AccountBlock, error) {
	var (
		methodData []byte
		err        error
	)
	if methodData, err = cancelPledgeRequest(db, address, pledgeType, amount); err != nil {
		return []*ledger.AccountBlock{}, err
	} else {
		return []*ledger.AccountBlock{
			{
				AccountAddress: block.AccountAddress,
				ToAddress:      types.AddressPledge,
				BlockType:      ledger.BlockTypeSendCall,
				TokenId:        ledger.ViteTokenId,
				Amount:         big.NewInt(0),
				Data:           methodData,
			},
		}, nil
	}
}

func pledgeRequest(db vm_db.VmDb, address types.Address, pledgeType uint8, amount *big.Int) ([]byte, error) {
	if pledgeType == PledgeForVip {
		if _, ok := GetPledgeForVip(db, address); ok {
			return nil, PledgeForVipExistsErr
		}
	}
	if dexFund, ok := GetUserFund(db, address); !ok {
		return nil, ExceedFundAvailableErr
	} else {
		account, exists := GetAccountByTokeIdFromFund(dexFund, ledger.ViteTokenId)
		if !exists || CmpForBigInt(account.Available, amount.Bytes()) < 0 {
			return nil, ExceedFundAvailableErr
		} else {
			account.Available = SubBigInt(account.Available, amount.Bytes()).Bytes()
			SaveUserFund(db, address, dexFund)
			if pledgeData, err := abi.ABIPledge.PackMethod(abi.MethodNameAgentPledge, address, types.AddressDexFund, pledgeType); err != nil {
				return nil, err
			} else {
				return pledgeData, err
			}
		}
	}
}

func cancelPledgeRequest(db vm_db.VmDb, address types.Address, pledgeType uint8, amount *big.Int) ([]byte, error) {
	if pledgeType == PledgeForVx {
		available := GetPledgeForVx(db, address)
		leave := new(big.Int).Sub(available, amount)
		if leave.Sign() < 0 || leave.Cmp(PledgeForVxMinAmount) < 0 {
			return nil, ExceedPledgeAvailableErr
		}
	} else {
		if pledgeVip, ok := GetPledgeForVip(db, address); !ok {
			return nil, PledgeForVipNotExistsErr
		} else {
			if pledgeVip.PledgeTimes == 1 && GetTimestampInt64(db)-pledgeVip.Timestamp < PledgeForVipDuration {
				return nil, PledgeForVipNotExpireErr
			}
		}
	}
	if cancelPledgeData, err := abi.ABIPledge.PackMethod(abi.MethodNameAgentCancelPledge, address, types.AddressDexFund, amount, uint8(pledgeType)); err != nil {
		return nil, err
	} else {
		return cancelPledgeData, err
	}
}

func newTokenInfoFromCallback(param *ParamDexFundGetTokenInfoCallback) *TokenInfo {
	tokenInfo := &TokenInfo{}
	tokenInfo.Decimals = int32(param.Decimals)
	tokenInfo.Symbol = param.TokenSymbol
	tokenInfo.Index = int32(param.Index)
	tokenInfo.Owner = param.Owner.Bytes()
	return tokenInfo
}

func ValidPrice(price string) bool {
	if len(price) == 0 {
		return false
	} else if pr, err := strconv.ParseFloat(price, 64); err != nil || pr <= 0 {
		return false
	} else if !checkPriceChar(price) {
		return false
	} else {
		idx := strings.Index(price, ".")
		if idx > priceIntMaxLen { // 10^12 < 2^40(5bytes)
			return false
		} else if idx >= 0 {
			if len(price)-(idx+1) > priceDecimalMaxLen { // // max price precision is 12 decimals 10^12 < 2^40(5bytes)
				return false
			}
		}
	}
	return true
}

func MaxTotalFeeRate(order Order) int32 {
	takerRate := order.TakerFeeRate + order.TakerBrokerFeeRate
	makerRate := order.MakerFeeRate + order.MakerBrokerFeeRate
	if takerRate > makerRate {
		return takerRate
	} else {
		return takerRate
	}
}

// only for unit test
func SetFeeRate(baseRate int32) {
	BaseFeeRate = baseRate
}

func ValidBrokerFeeRate(feeRate int32) bool {
	return feeRate >= 0 && feeRate <= MaxBrokerFeeRate
}

func checkPriceChar(price string) bool {
	for _, v := range price {
		if v != '.' && (uint8(v) < '0' || uint8(v) > '9') {
			return false
		}
	}
	return true
}

func getDexTokenSymbol(tokenInfo *TokenInfo) string {
	indexStr := strconv.Itoa(int(tokenInfo.Index))
	for ; len(indexStr) < 3; {
		indexStr = "0" + indexStr
	}
	return fmt.Sprintf("%s-%s", tokenInfo.Symbol, indexStr)
}
