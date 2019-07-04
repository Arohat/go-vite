/*
 * Copyright 2019 The go-vite Authors
 * This file is part of the go-vite library.
 *
 * The go-vite library is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * The go-vite library is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Lesser General Public License for more details.
 *
 * You should have received a copy of the GNU Lesser General Public License
 * along with the go-vite library. If not, see <http://www.gnu.org/licenses/>.
 */

package p2p

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vitelabs/go-vite/common/types"

	"github.com/vitelabs/go-vite/net/vnode"

	"github.com/vitelabs/go-vite/log15"
)

type PeerInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    int    `json:"version"`
	Height     uint64 `json:"height"`
	Address    string `json:"address"`
	Level      Level  `json:"level"`
	CreateAt   string `json:"createAt"`
	ReadQueue  int    `json:"readQueue"`
	WriteQueue int    `json:"writeQueue"`
}

const peerReadMsgBufferSize = 10
const peerWriteMsgBufferSize = 100

type Peer struct {
	codec       Codec
	Id          vnode.NodeID
	Name        string
	Height      uint64
	Head        types.Hash
	Version     int
	Flag        PeerFlag
	Superior    bool
	manager     PeerManager
	CreateAt    int64
	unning      int32
	writable    int32 // set to 0 when write error in writeLoop, or close actively
	writing     int32
	readQueue   chan Msg // will be closed when read error in readLoop
	writeQueue  chan Msg // will be closed in method Close
	errChan     chan error
	wg          sync.WaitGroup
	log         log15.Logger
	proto       Protocol
	fileAddress string
}

func (p *Peer) Weight() int64 {
	return int64(p.level)
}

func (p *Peer) Head() types.Hash {
	return p.head
}

func (p *Peer) SetHead(head types.Hash, height uint64) {
	p.head, p.height = head, height
}

// Level return the peer`s level
func (p *Peer) Level() Level {
	return p.level
}

// SetLevel change the peer`s level, return error is not nil if peer is not running, or change failed
func (p *Peer) SetLevel(level Level) error {
	if atomic.LoadInt32(&p.running) == 0 {
		return errPeerNotRunning
	}

	old := p.level
	p.level = level

	err := p.pm.changeLevel(p, old)
	if err != nil {
		p.log.Warn(fmt.Sprintf("failed to change peer %s from level %d to level %d", p.Address(), old, level))
	}

	return err
}

// String return `id@address`
func (p *Peer) String() string {
	return p.id.Brief() + "@" + p.codec.Address().String()
}

// Address return the remote net address
func (p *Peer) Address() net.Addr {
	return p.codec.Address()
}

func NewPeer(id vnode.NodeID, name string, height uint64, head types.Hash, fileAddress string, version int, c Codec, level Level, proto Protocol) *Peer {
	pm := &Peer{
		codec:       c,
		id:          id,
		name:        name,
		version:     version,
		level:       level,
		createAt:    time.Now(),
		readQueue:   make(chan Msg, peerReadMsgBufferSize),
		writeQueue:  make(chan Msg, peerWriteMsgBufferSize),
		running:     0,
		writable:    1,
		errChan:     make(chan error, 3),
		log:         p2pLog.New("peer", id.Brief()),
		proto:       proto,
		fileAddress: fileAddress,
		height:      height,
		head:        head,
	}

	return pm
}

func (p *Peer) ID() vnode.NodeID {
	return p.id
}

func (p *Peer) Height() uint64 {
	return p.height
}

func (p *Peer) FileAddress() string {
	return p.fileAddress
}

// setManager will be invoked before run by module p2p
func (p *Peer) setManager(pm levelManager) {
	p.pm = pm
}

func (p *Peer) run() (err error) {
	if atomic.CompareAndSwapInt32(&p.running, 0, 1) {
		err = p.onAdded()
		if err != nil {
			return
		}

		defer p.onRemoved()

		p.goLoop(p.readLoop, p.errChan)
		p.goLoop(p.writeLoop, p.errChan)
		p.goLoop(p.handleLoop, p.errChan)

		err = <-p.errChan
		return
	}

	return errPeerAlreadyRunning
}

func (p *Peer) goLoop(fn func() error, ch chan<- error) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		err := fn()
		ch <- err
	}()
}

func (p *Peer) readLoop() (err error) {
	defer close(p.readQueue)

	var msg Msg

	for {
		p.log.Debug(fmt.Sprintf("begin read message"))
		msg, err = p.codec.ReadMsg()
		p.log.Debug(fmt.Sprintf("read message %d %d bytes done", msg.Code, len(msg.Payload)))
		if err != nil {
			atomic.StoreInt32(&p.writable, 0)
			return
		}

		msg.ReceivedAt = time.Now()
		msg.Sender = p

		switch msg.Code {
		case CodeDisconnect:
			if len(msg.Payload) > 0 {
				err = PeerError(msg.Payload[0])
			} else {
				err = PeerUnknownReason
			}
			return
		case CodeControlFlow:
		// todo

		default:
			p.readQueue <- msg
		}
	}
}

func (p *Peer) writeLoop() (err error) {
	var msg Msg
	for msg = range p.writeQueue {
		t1 := time.Now()
		p.log.Debug(fmt.Sprintf("begin write msg %d %d bytes", msg.Code, len(msg.Payload)))
		if err = p.codec.WriteMsg(msg); err != nil {
			p.log.Debug(fmt.Sprintf("write msg %d %d bytes error: %v", msg.Code, len(msg.Payload), err))
			atomic.StoreInt32(&p.writable, 0)
			return
		}
		p.log.Debug(fmt.Sprintf("write msg %d %d bytes done[%d][%s]", msg.Code, len(msg.Payload), len(p.writeQueue), time.Now().Sub(t1)))
	}

	return nil
}

func (p *Peer) handleLoop() (err error) {
	var msg Msg
	for msg = range p.readQueue {
		t1 := time.Now()
		p.log.Debug(fmt.Sprintf("begin handle msg %d", msg.Code))
		err = p.proto.Handle(msg)
		p.log.Debug(fmt.Sprintf("handle msg %d done[%d][%s]", msg.Code, len(p.readQueue), time.Now().Sub(t1)))
		if err != nil {
			return
		}
	}

	return nil
}

func (p *Peer) Close(err error) (err2 error) {
	if atomic.CompareAndSwapInt32(&p.running, 1, 0) {
		if pe, ok := err.(PeerError); ok {
			_ = p.WriteMsg(Msg{
				Code:    CodeDisconnect,
				Payload: []byte{byte(pe)},
			})
		}

		time.Sleep(100 * time.Millisecond)
		atomic.StoreInt32(&p.writable, 0)

		// ensure nobody is writing
		for {
			if atomic.LoadInt32(&p.writing) == 0 {
				break
			}

			time.Sleep(100 * time.Millisecond)
		}

		close(p.writeQueue)

		if err3 := p.codec.Close(); err3 != nil {
			err2 = err3
		}

		p.wg.Wait()
	}

	return errPeerNotRunning
}

func (p *Peer) Disconnect(err error) {
	_ = Disconnect(p.codec, err)
}

func (p *Peer) onAdded() (err error) {
	err = p.proto.OnPeerAdded(p)
	if err != nil {
		p.log.Error(fmt.Sprintf("failed to add peer %s: %v", p, err))
	}

	return
}

func (p *Peer) onRemoved() {
	err := p.proto.OnPeerRemoved(p)
	if err != nil {
		p.log.Error(fmt.Sprintf("failed to remove peer %s: %v", p, err))
	}

	return
}

func (p *Peer) Info() PeerInfo {
	return PeerInfo{
		ID:         p.id.String(),
		Name:       p.name,
		Version:    p.version,
		Height:     p.height,
		Address:    p.codec.Address().String(),
		Level:      p.level,
		CreateAt:   p.createAt.Format("2006-01-02 15:04:05"),
		ReadQueue:  len(p.readQueue),
		WriteQueue: len(p.writeQueue),
	}
}
