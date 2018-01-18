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

package stream

import (
	"context"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/protocols"
	pq "github.com/ethereum/go-ethereum/swarm/network/priorityqueue"
	"github.com/ethereum/go-ethereum/swarm/storage"
)

// Peer is the Peer extention for the streaming protocol
type Peer struct {
	*protocols.Peer
	streamer   *Registry
	pq         *pq.PriorityQueue
	outgoingMu sync.RWMutex
	incomingMu sync.RWMutex
	servers    map[string]*server
	clients    map[string]*client
	quit       chan struct{}
}

// NewPeer is the constructor for Peer
func NewPeer(peer *protocols.Peer, streamer *Registry) *Peer {
	p := &Peer{
		Peer:     peer,
		pq:       pq.New(int(PriorityQueue), PriorityQueueCap),
		streamer: streamer,
		servers:  make(map[string]*server),
		clients:  make(map[string]*client),
		quit:     make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go p.pq.Run(ctx, func(i interface{}) { p.Send(i) })
	go func() {
		<-p.quit
		cancel()
	}()
	return p
}

// Deliver sends a storeRequestMsg protocol message to the peer
func (p *Peer) Deliver(chunk *storage.Chunk, priority uint8) error {
	msg := &ChunkDeliveryMsg{
		Key:   chunk.Key,
		SData: chunk.SData,
	}
	return p.pq.Push(nil, msg, int(priority))
}

// Deliver sends a storeRequestMsg protocol message to the peer
func (p *Peer) SendPriority(msg interface{}, priority uint8) error {
	return p.pq.Push(nil, msg, int(priority))
}

// SendOfferedHashes sends OfferedHashesMsg protocol msg
func (p *Peer) SendOfferedHashes(s *server, f, t uint64) error {
	hashes, from, to, proof, err := s.SetNextBatch(f, t)
	if err != nil {
		return err
	}
	if proof == nil {
		proof = &HandoverProof{
			Handover: &Handover{},
		}
	}
	s.currentBatch = hashes
	msg := &OfferedHashesMsg{
		HandoverProof: proof,
		Hashes:        hashes,
		From:          from,
		To:            to,
		Stream:        s.stream,
		Key:           s.key,
	}
	log.Debug("Swarm syncer offer batch", "stream", s.stream, "key", s.key, "len", len(hashes), "from", from, "to", to)
	return p.SendPriority(msg, s.priority)
}

func (p *Peer) getServer(s string) (*server, error) {
	p.outgoingMu.RLock()
	defer p.outgoingMu.RUnlock()

	server := p.servers[s]
	if server == nil {
		return nil, fmt.Errorf("server '%v' not provided to peer %v", s, p.ID())
	}
	return server, nil
}

func (p *Peer) getClient(s string) (*client, error) {
	p.incomingMu.RLock()
	defer p.incomingMu.RUnlock()

	client := p.clients[s]
	if client == nil {
		return nil, fmt.Errorf("client '%v' not provided to peer %v", s, p.ID())
	}
	return client, nil
}

func (p *Peer) setServer(s string, key []byte, o Server, priority uint8) (*server, error) {
	p.outgoingMu.Lock()
	defer p.outgoingMu.Unlock()

	sk := s + keyToString(key)
	if p.servers[sk] != nil {
		return nil, fmt.Errorf("server %v already registered", sk)
	}
	os := &server{
		Server:   o,
		priority: priority,
		stream:   s,
		key:      key,
	}
	p.servers[sk] = os
	return os, nil
}

func (p *Peer) setClient(s string, key []byte, i Client, priority uint8, live bool) error {
	p.incomingMu.Lock()
	defer p.incomingMu.Unlock()

	sk := s + keyToString(key)
	if p.clients[sk] != nil {
		return fmt.Errorf("client %v already registered", sk)
	}
	next := make(chan struct{}, 1)
	// var intervals *Intervals
	// if !live {
	// key := s + p.ID().String()
	// intervals = NewIntervals(key, p.streamer)
	// }
	p.clients[sk] = &client{
		Client: i,
		// intervals:        intervals,
		live:     live,
		priority: priority,
		next:     next,
		stream:   s,
		key:      key,
	}
	next <- struct{}{} // this is to allow wantedKeysMsg before first batch arrives
	return nil
}
