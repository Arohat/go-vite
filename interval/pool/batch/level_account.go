package batch

import (
	"github.com/go-errors/errors"
	"github.com/vitelabs/go-vite/interval/common"
)

type accountLevel struct {
	level
	bs    map[common.Address]*bucket
	sHash *common.Hash
}

func (self *accountLevel) Buckets() (result []Bucket) {
	for _, v := range self.bs {
		result = append(result, v)
	}
	return
}

func newAccountLevel(index int, sHash *common.Hash) *accountLevel {
	return &accountLevel{bs: make(map[common.Address]*bucket), level: level{index: index}, sHash: sHash}
}

func (self *accountLevel) Snapshot() bool {
	return false
}

func (self *accountLevel) SHash() *common.Hash {
	return self.sHash
}

func (self *accountLevel) Add(b Item) error {
	if self.Closed() {
		panic(errors.New("level is closed"))
	}
	owner := *b.Owner()
	_, ok := self.bs[owner]
	if !ok {
		self.bs[owner] = newBucket(&owner)
	}
	return self.bs[owner].add(b)
}

func (self *accountLevel) Size() int {
	num := 0
	for _, v := range self.bs {
		num += len(v.Items())
	}

	return num
}
