package kvsrv

import (
	"log"
	"sync"

	"6.5840/kvsrv1/rpc"
	"6.5840/labrpc"
	"6.5840/tester1"
)

const Debug = false

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug {
		log.Printf(format, a...)
	}
	return
}

type KVEntry struct {
	Value   string
	Version rpc.Tversion
}

type KVServer struct {
	mu sync.Mutex

	data map[string]*KVEntry
}

func MakeKVServer() *KVServer {
	kv := &KVServer{
		data: make(map[string]*KVEntry),
	}
	return kv
}

func (kv *KVServer) Get(args *rpc.GetArgs, reply *rpc.GetReply) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	entry, ok := kv.data[args.Key]
	if !ok {
		reply.Err = rpc.ErrNoKey
		return
	}

	reply.Value = entry.Value
	reply.Version = entry.Version
	reply.Err = rpc.OK
}

func (kv *KVServer) Put(args *rpc.PutArgs, reply *rpc.PutReply) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	entry, ok := kv.data[args.Key]

	if !ok {
		if args.Version == 0 {
			kv.data[args.Key] = &KVEntry{
				Value:   args.Value,
				Version: 1,
			}
			reply.Err = rpc.OK
		} else {
			reply.Err = rpc.ErrNoKey
		}
		return
	}

	if args.Version != entry.Version {
		reply.Err = rpc.ErrVersion
		return
	}

	entry.Value = args.Value
	entry.Version++
	reply.Err = rpc.OK
}

func StartKVServer(tc *tester.TesterClnt, ends []*labrpc.ClientEnd, gid tester.Tgid, srv int, persister *tester.Persister) []any {
	kv := MakeKVServer()
	return []any{kv}
}
