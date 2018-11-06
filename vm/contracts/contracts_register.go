package contracts

import (
	"errors"
	"math/big"

	"github.com/vitelabs/go-vite/common/helper"
	"github.com/vitelabs/go-vite/common/types"
	"github.com/vitelabs/go-vite/consensus/core"
	"github.com/vitelabs/go-vite/ledger"
	cabi "github.com/vitelabs/go-vite/vm/contracts/abi"
	"github.com/vitelabs/go-vite/vm/util"
	"github.com/vitelabs/go-vite/vm_context/vmctxt_interface"
)

type MethodRegister struct {
}

func (p *MethodRegister) GetFee(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (p *MethodRegister) GetRefundData() []byte {
	return []byte{1}
}

// register to become a super node of a consensus group, lock 1 million ViteToken for 3 month
func (p *MethodRegister) DoSend(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, quotaLeft uint64) (uint64, error) {
	quotaLeft, err := util.UseQuota(quotaLeft, RegisterGas)
	if err != nil {
		return quotaLeft, err
	}

	param := new(cabi.ParamRegister)
	if err = cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameRegister, block.Data); err != nil {
		return quotaLeft, util.ErrInvalidMethodParam
	}
	if param.Gid == types.DELEGATE_GID {
		return quotaLeft, errors.New("cannot register consensus group")
	}
	if err = checkRegisterData(cabi.MethodNameRegister, db, block, param); err != nil {
		return quotaLeft, err
	}
	return quotaLeft, nil
}

func checkRegisterData(methodName string, db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, param *cabi.ParamRegister) error {
	consensusGroupInfo := cabi.GetConsensusGroup(db, param.Gid)
	if consensusGroupInfo == nil {
		return errors.New("consensus group not exist")
	}
	if condition, ok := getConsensusGroupCondition(consensusGroupInfo.RegisterConditionId, cabi.RegisterConditionPrefix); !ok {
		return errors.New("register condition id not exist")
	} else if !condition.checkData(consensusGroupInfo.RegisterConditionParam, db, block, param, methodName) {
		return errors.New("register condition not match")
	}
	return nil
}

func (p *MethodRegister) DoReceive(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, sendBlock *ledger.AccountBlock) ([]*SendBlock, error) {
	param := new(cabi.ParamRegister)
	cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameRegister, sendBlock.Data)

	// Registration is not exist
	// or registration is not active and belongs to sender account
	snapshotBlock := db.CurrentSnapshotBlock()

	var rewardIndex = uint64(0)
	var err error
	if param.Gid == types.SNAPSHOT_GID {
		groupInfo := cabi.GetConsensusGroup(db, param.Gid)
		reader := core.NewReader(*db.GetGenesisSnapshotBlock().Timestamp, groupInfo)
		// TODO Why TimeToIndex returns error
		rewardIndex, err = reader.TimeToIndex(*snapshotBlock.Timestamp)
		if err != nil {
			return nil, err
		}
	}
	key := cabi.GetRegisterKey(param.Name, param.Gid)
	oldData := db.GetStorage(&block.AccountAddress, key)
	var hisAddrList []types.Address
	if len(oldData) > 0 {
		old := new(types.Registration)
		cabi.ABIRegister.UnpackVariable(old, cabi.VariableNameRegistration, oldData)
		if old.IsActive() || old.PledgeAddr != sendBlock.AccountAddress {
			return nil, errors.New("register data exist")
		}
		// TODO check reward of last being a super node is not drained?
		hisAddrList = old.HisAddrList
	}

	// Node addr belong to one name in a consensus group
	hisNameKey := cabi.GetHisNameKey(param.NodeAddr, param.Gid)
	hisName := new(string)
	err = cabi.ABIRegister.UnpackVariable(hisName, cabi.VariableNameHisName, db.GetStorage(&block.AccountAddress, hisNameKey))
	if err == nil && *hisName != param.Name {
		return nil, errors.New("node address is registered to another name before")
	}
	if err != nil {
		// hisName not exist, update hisName
		hisAddrList = append(hisAddrList, param.NodeAddr)
		hisNameData, _ := cabi.ABIRegister.PackVariable(cabi.VariableNameHisName, param.Name)
		db.SetStorage(hisNameKey, hisNameData)
	}

	registerInfo, _ := cabi.ABIRegister.PackVariable(
		cabi.VariableNameRegistration,
		param.Name,
		param.NodeAddr,
		sendBlock.AccountAddress,
		sendBlock.Amount,
		getRegisterWithdrawHeight(db, param.Gid, snapshotBlock.Height),
		rewardIndex,
		uint64(0),
		hisAddrList)
	db.SetStorage(key, registerInfo)
	return nil, nil
}

func getRegisterWithdrawHeight(db vmctxt_interface.VmDatabase, gid types.Gid, currentHeight uint64) uint64 {
	consensusGroupInfo := cabi.GetConsensusGroup(db, gid)
	withdrawHeight := getRegisterWithdrawHeightByCondition(consensusGroupInfo.RegisterConditionId, consensusGroupInfo.RegisterConditionParam, currentHeight)
	return withdrawHeight
}

type MethodCancelRegister struct {
}

func (p *MethodCancelRegister) GetFee(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (p *MethodCancelRegister) GetRefundData() []byte {
	return []byte{2}
}

// cancel register to become a super node of a consensus group after registered for 3 month, get 100w ViteToken back
func (p *MethodCancelRegister) DoSend(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, quotaLeft uint64) (uint64, error) {
	quotaLeft, err := util.UseQuota(quotaLeft, CancelRegisterGas)
	if err != nil {
		return quotaLeft, err
	}

	param := new(cabi.ParamCancelRegister)
	if err = cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameCancelRegister, block.Data); err != nil {
		return quotaLeft, util.ErrInvalidMethodParam
	}

	consensusGroupInfo := cabi.GetConsensusGroup(db, param.Gid)
	if consensusGroupInfo == nil {
		return quotaLeft, errors.New("consensus group not exist")
	}
	if condition, ok := getConsensusGroupCondition(consensusGroupInfo.RegisterConditionId, cabi.RegisterConditionPrefix); !ok {
		return quotaLeft, errors.New("consensus group register condition not exist")
	} else if !condition.checkData(consensusGroupInfo.RegisterConditionParam, db, block, param, cabi.MethodNameCancelRegister) {
		return quotaLeft, errors.New("check register condition failed")
	}
	return quotaLeft, nil
}
func (p *MethodCancelRegister) DoReceive(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, sendBlock *ledger.AccountBlock) ([]*SendBlock, error) {
	param := new(cabi.ParamCancelRegister)
	cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameCancelRegister, sendBlock.Data)

	snapshotBlock := db.CurrentSnapshotBlock()

	key := cabi.GetRegisterKey(param.Name, param.Gid)
	old := new(types.Registration)
	err := cabi.ABIRegister.UnpackVariable(
		old,
		cabi.VariableNameRegistration,
		db.GetStorage(&block.AccountAddress, key))
	if err != nil || !old.IsActive() || old.PledgeAddr != sendBlock.AccountAddress || old.WithdrawHeight > snapshotBlock.Height {
		return nil, errors.New("registration status error")
	}

	// update lock amount and loc start height
	registerInfo, _ := cabi.ABIRegister.PackVariable(
		cabi.VariableNameRegistration,
		old.Name,
		old.NodeAddr,
		old.PledgeAddr,
		helper.Big0,
		uint64(0),
		old.RewardIndex,
		snapshotBlock.Height,
		old.HisAddrList)
	db.SetStorage(key, registerInfo)
	// return locked ViteToken
	if old.Amount.Sign() > 0 {
		return []*SendBlock{
			{
				block,
				sendBlock.AccountAddress,
				ledger.BlockTypeSendCall,
				old.Amount,
				ledger.ViteTokenId,
				[]byte{},
			},
		}, nil
	}
	return nil, nil
}

type MethodReward struct {
}

func (p *MethodReward) GetFee(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock) (*big.Int, error) {
	return big.NewInt(0), nil
}

func (p *MethodReward) GetRefundData() []byte {
	return []byte{3}
}

// get reward of generating snapshot block
func (p *MethodReward) DoSend(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, quotaLeft uint64) (uint64, error) {
	quotaLeft, err := util.UseQuota(quotaLeft, RewardGas)
	if err != nil {
		return quotaLeft, err
	}
	if block.Amount.Sign() != 0 ||
		!IsUserAccount(db, block.AccountAddress) {
		return quotaLeft, errors.New("invalid block data")
	}
	param := new(cabi.ParamReward)
	if err = cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameReward, block.Data); err != nil {
		return quotaLeft, util.ErrInvalidMethodParam
	}
	if !util.IsSnapshotGid(param.Gid) {
		return quotaLeft, errors.New("consensus group has no reward")
	}
	return quotaLeft, nil
}
func (p *MethodReward) DoReceive(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, sendBlock *ledger.AccountBlock) ([]*SendBlock, error) {
	param := new(cabi.ParamReward)
	cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameReward, sendBlock.Data)
	key := cabi.GetRegisterKey(param.Name, param.Gid)
	old := new(types.Registration)
	err := cabi.ABIRegister.UnpackVariable(old, cabi.VariableNameRegistration, db.GetStorage(&block.AccountAddress, key))
	if err != nil || sendBlock.AccountAddress != old.PledgeAddr {
		return nil, errors.New("invalid owner")
	}
	_, endIndex, reward, err := CalcReward(db, old, param.Gid)
	if err != nil {
		panic(err)
	}
	if endIndex != old.RewardIndex {
		registerInfo, _ := cabi.ABIRegister.PackVariable(
			cabi.VariableNameRegistration,
			old.Name,
			old.NodeAddr,
			old.PledgeAddr,
			old.Amount,
			old.WithdrawHeight,
			endIndex,
			old.CancelHeight,
			old.HisAddrList)
		db.SetStorage(key, registerInfo)

		if reward != nil && reward.Sign() > 0 {
			return []*SendBlock{
				{
					block,
					param.BeneficialAddr,
					ledger.BlockTypeSendReward,
					reward,
					ledger.ViteTokenId,
					[]byte{},
				},
			}, nil
		}
	}
	return nil, nil
}

func CalcReward(db vmctxt_interface.VmDatabase, old *types.Registration, gid types.Gid) (uint64, uint64, *big.Int, error) {
	currentSnapshotBlock := db.CurrentSnapshotBlock()
	genesisTime := db.GetGenesisSnapshotBlock().Timestamp
	groupInfo := cabi.GetConsensusGroup(db, gid)
	if groupInfo == nil {
		return old.RewardIndex, old.RewardIndex, big.NewInt(0), errors.New("consensus group info not exist")
	}
	reader := core.NewReader(*genesisTime, groupInfo)

	if currentSnapshotBlock.Height < nodeConfig.params.RewardEndHeightLimit ||
		old.RewardIndex == 0 {
		return old.RewardIndex, old.RewardIndex, big.NewInt(0), nil
	}
	var cancelIndex = uint64(0)
	var err error
	if !old.IsActive() {
		cancelIndex, err = reader.TimeToIndex(*db.GetSnapshotBlockByHeight(old.CancelHeight).Timestamp)
		if err != nil {
			return old.RewardIndex, old.RewardIndex, big.NewInt(0), err
		}
		cancelIndex = cancelIndex
		if old.RewardIndex >= cancelIndex {
			return old.RewardIndex, old.RewardIndex, big.NewInt(0), nil
		}
	}

	indexPeriodTime, err := reader.PeriodTime()
	if err != nil {
		return old.RewardIndex, old.RewardIndex, big.NewInt(0), err
	}
	indexPerDay := SecondPerDay / indexPeriodTime

	startIndex, err := reader.TimeToIndex(*db.GetSnapshotBlockByHeight(old.RewardIndex).Timestamp)
	startIndex = startIndex + 1

	endIndex := uint64(0)
	if !old.IsActive() {
		endIndex = cancelIndex
	} else {
		endHeight := db.CurrentSnapshotBlock().Height - nodeConfig.params.RewardEndHeightLimit
		endIndex, err = reader.TimeToIndex(*db.GetSnapshotBlockByHeight(endHeight).Timestamp)
		if err != nil {
			return old.RewardIndex, old.RewardIndex, big.NewInt(0), err
		}
	}

	indexCount := uint64(0)
	if endIndexLimit := startIndex + indexPerDay*RewardDayLimit; endIndex >= endIndexLimit {
		indexCount = indexPerDay * RewardDayLimit
		endIndex = endIndexLimit
	} else if !old.IsActive() {
		indexCount = indexPerDay * ((endIndex - startIndex + indexPerDay - 1) / indexPerDay)
	} else {
		indexCount = indexPerDay * ((endIndex - startIndex + indexPerDay - 1) / indexPerDay)
		endIndex = startIndex + indexPerDay*indexCount
	}

	if endIndex <= startIndex {
		return old.RewardIndex, old.RewardIndex, big.NewInt(0), nil
	}

	rewardF := new(big.Float).SetPrec(rewardPrecForFloat).SetInt64(0)
	tmp1 := new(big.Float).SetPrec(rewardPrecForFloat).SetInt64(0)
	tmp2 := new(big.Float).SetPrec(rewardPrecForFloat).SetInt64(0)
	tmp3 := new(big.Float).SetPrec(rewardPrecForFloat).SetInt64(0)
	for indexCount > 0 {
		var dayInfo *core.Detail
		if indexCount < indexPerDay {
			dayInfo, err = reader.VoteDetails(startIndex, startIndex+indexCount, old, nil)
			indexCount = 0
		} else {
			dayInfo, err = reader.VoteDetails(startIndex, startIndex+indexPerDay, old, nil)
			indexCount = indexCount - indexPerDay
			startIndex = startIndex + indexPerDay
		}
		if dayInfo.ActualNum == 0 {
			continue
		}

		for _, periodInfo := range dayInfo.PeriodM {
			if periodInfo.ActualNum == 0 {
				continue
			}
			if voteCount, ok := periodInfo.VoteMap[old.Name]; ok && voteCount.Sign() > 0 {
				totalVoteCount := big.NewInt(0)
				for _, voteCount := range periodInfo.VoteMap {
					totalVoteCount.Add(totalVoteCount, voteCount)
				}
				tmp1.Quo(tmp1.SetInt(voteCount), tmp2.SetInt(totalVoteCount))
				tmp1.Mul(tmp1, tmp2.SetUint64(periodInfo.ActualNum))
				tmp3.Add(tmp3, tmp1)
			}
		}
		tmp1.Quo(tmp2.SetUint64(dayInfo.ActualNum), tmp1.SetUint64(dayInfo.PlanNum))
		tmp1.Mul(tmp1, tmp3)
		tmp1.Add(tmp1, float1)
		tmp1.Mul(tmp1, tmp2)
		rewardF.Add(rewardF, tmp1)

		tmp3.SetUint64(0)
	}
	reward, _ := new(big.Int).SetString(rewardF.Text('f', 0), 10)
	if reward.Sign() > 0 {
		reward.Mul(reward, rewardPerBlock)
		reward.Quo(reward, helper.Big2)
	}
	return old.RewardIndex, endIndex, reward, nil
}

type MethodUpdateRegistration struct {
}

func (p *MethodUpdateRegistration) GetFee(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock) (*big.Int, error) {
	return big.NewInt(0), nil
}

func (p *MethodUpdateRegistration) GetRefundData() []byte {
	return []byte{4}
}

// update registration info
func (p *MethodUpdateRegistration) DoSend(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, quotaLeft uint64) (uint64, error) {
	quotaLeft, err := util.UseQuota(quotaLeft, UpdateRegistrationGas)
	if err != nil {
		return quotaLeft, err
	}

	param := new(cabi.ParamRegister)
	if err = cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameUpdateRegistration, block.Data); err != nil {
		return quotaLeft, util.ErrInvalidMethodParam
	}

	if err = checkRegisterData(cabi.MethodNameUpdateRegistration, db, block, param); err != nil {
		return quotaLeft, err
	}
	return quotaLeft, nil
}
func (p *MethodUpdateRegistration) DoReceive(db vmctxt_interface.VmDatabase, block *ledger.AccountBlock, sendBlock *ledger.AccountBlock) ([]*SendBlock, error) {
	param := new(cabi.ParamRegister)
	cabi.ABIRegister.UnpackMethod(param, cabi.MethodNameUpdateRegistration, sendBlock.Data)

	key := cabi.GetRegisterKey(param.Name, param.Gid)
	old := new(types.Registration)
	err := cabi.ABIRegister.UnpackVariable(old, cabi.VariableNameRegistration, db.GetStorage(&block.AccountAddress, key))
	if err != nil || !old.IsActive() || old.PledgeAddr != sendBlock.AccountAddress {
		return nil, errors.New("register not exist or already canceled")
	}
	// check node addr belong to one name in a consensus group
	hisNameKey := cabi.GetHisNameKey(param.NodeAddr, param.Gid)
	hisName := new(string)
	err = cabi.ABIRegister.UnpackVariable(hisName, cabi.VariableNameHisName, db.GetStorage(&block.AccountAddress, hisNameKey))
	if err == nil && *hisName != param.Name {
		// hisName exist
		return nil, errors.New("node address is registered to another name before")
	}
	if err != nil {
		// hisName not exist, update hisName
		old.HisAddrList = append(old.HisAddrList, param.NodeAddr)
		hisNameData, _ := cabi.ABIRegister.PackVariable(cabi.VariableNameHisName, param.Name)
		db.SetStorage(hisNameKey, hisNameData)
	}
	registerInfo, _ := cabi.ABIRegister.PackVariable(
		cabi.VariableNameRegistration,
		old.Name,
		param.NodeAddr,
		old.PledgeAddr,
		old.Amount,
		old.WithdrawHeight,
		old.RewardIndex,
		old.CancelHeight,
		old.HisAddrList)
	db.SetStorage(key, registerInfo)
	return nil, nil
}
