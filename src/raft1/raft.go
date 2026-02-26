package raft

import (
	"bytes"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raftapi"
	"6.5840/tester1"
)

const (
	Follower = iota
	Candidate
	Leader
)

type LogEntry struct {
	Term    int
	Command interface{}
}

type Raft struct {
	mu        sync.Mutex
	peers     []*labrpc.ClientEnd
	persister *tester.Persister
	me        int
	dead      int32

	currentTerm int
	votedFor    int
	log         []LogEntry

	commitIndex int
	lastApplied int

	nextIndex  []int
	matchIndex []int

	state       int
	lastContact time.Time

	applyCh chan raftapi.ApplyMsg
}

func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.state == Leader
}

func (rf *Raft) persist() {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.log)
	raftstate := w.Bytes()
	rf.persister.Save(raftstate, nil)
}

func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 {
		return
	}
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentTerm int
	var votedFor int
	var log []LogEntry
	if d.Decode(&currentTerm) != nil ||
		d.Decode(&votedFor) != nil ||
		d.Decode(&log) != nil {
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = log
	}
}

func (rf *Raft) PersistBytes() int {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.persister.RaftStateSize()
}

func (rf *Raft) Snapshot(index int, snapshot []byte) {
}

type RequestVoteArgs struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

type RequestVoteReply struct {
	Term        int
	VoteGranted bool
}

func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.VoteGranted = false

	if args.Term < rf.currentTerm {
		return
	}

	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
	}

	lastLogIndex := len(rf.log) - 1
	lastLogTerm := 0
	if lastLogIndex >= 0 {
		lastLogTerm = rf.log[lastLogIndex].Term
	}

	upToDate := args.LastLogTerm > lastLogTerm ||
		(args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex)

	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && upToDate {
		rf.votedFor = args.CandidateId
		rf.state = Follower
		rf.lastContact = time.Now()
		reply.VoteGranted = true
		rf.persist()
	}
}

func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term    int
	Success bool
	XTerm   int
	XIndex  int
	XLen    int
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.Success = false

	if args.Term < rf.currentTerm {
		return
	}

	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist()
	}

	rf.lastContact = time.Now()
	rf.state = Follower

	if args.PrevLogIndex >= len(rf.log) {
		reply.XTerm = -1
		reply.XIndex = -1
		reply.XLen = len(rf.log)
		return
	}

	if args.PrevLogIndex >= 0 && rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		reply.XTerm = rf.log[args.PrevLogIndex].Term
		reply.XIndex = args.PrevLogIndex
		for i := args.PrevLogIndex - 1; i >= 0; i-- {
			if rf.log[i].Term != reply.XTerm {
				reply.XIndex = i + 1
				break
			}
		}
		reply.XLen = len(rf.log)
		return
	}

	reply.Success = true

	for i, entry := range args.Entries {
		idx := args.PrevLogIndex + 1 + i
		if idx >= len(rf.log) {
			rf.log = append(rf.log, entry)
		} else {
			if rf.log[idx].Term != entry.Term {
				rf.log = rf.log[:idx]
				rf.log = append(rf.log, entry)
			}
		}
	}

	if len(args.Entries) > 0 {
		rf.persist()
	}

	if args.LeaderCommit > rf.commitIndex {
		lastNewIndex := args.PrevLogIndex + len(args.Entries)
		if lastNewIndex < args.LeaderCommit {
			rf.commitIndex = lastNewIndex
		} else {
			rf.commitIndex = args.LeaderCommit
		}
	}
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return -1, rf.currentTerm, false
	}

	index := len(rf.log)
	entry := LogEntry{Term: rf.currentTerm, Command: command}
	rf.log = append(rf.log, entry)
	rf.persist()

	return index, rf.currentTerm, true
}

func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

func (rf *Raft) ticker() {
	for !rf.killed() {
		rf.mu.Lock()
		now := time.Now()
		timeout := time.Duration(300+rand.Int63()%150) * time.Millisecond
		elapsed := now.Sub(rf.lastContact)

		shouldElection := rf.state != Leader && elapsed > timeout
		rf.mu.Unlock()

		if shouldElection {
			rf.startElection()
		}

		ms := 50 + (rand.Int63() % 50)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

func (rf *Raft) startElection() {
	rf.mu.Lock()
	rf.state = Candidate
	rf.currentTerm++
	rf.votedFor = rf.me
	currentTerm := rf.currentTerm
	rf.persist()
	rf.lastContact = time.Now()

	lastLogIndex := len(rf.log) - 1
	lastLogTerm := 0
	if lastLogIndex >= 0 {
		lastLogTerm = rf.log[lastLogIndex].Term
	}

	args := RequestVoteArgs{
		Term:         currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
	}
	rf.mu.Unlock()

	votes := int32(1)

	for i := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			reply := RequestVoteReply{}
			ok := rf.sendRequestVote(server, &args, &reply)
			if ok {
				rf.mu.Lock()
				defer rf.mu.Unlock()
				if rf.state == Candidate && rf.currentTerm == args.Term {
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.votedFor = -1
						rf.state = Follower
						rf.persist()
					} else if reply.VoteGranted {
						atomic.AddInt32(&votes, 1)
						if atomic.LoadInt32(&votes) > int32(len(rf.peers)/2) {
							rf.state = Leader
							rf.nextIndex = make([]int, len(rf.peers))
							rf.matchIndex = make([]int, len(rf.peers))
							for j := range rf.nextIndex {
								rf.nextIndex[j] = len(rf.log)
								rf.matchIndex[j] = 0
							}
							rf.matchIndex[rf.me] = len(rf.log) - 1
						}
					}
				}
			}
		}(i)
	}
}

func (rf *Raft) sendHeartbeats() {
	rf.mu.Lock()
	if rf.state != Leader {
		rf.mu.Unlock()
		return
	}
	currentTerm := rf.currentTerm
	commitIndex := rf.commitIndex
	rf.mu.Unlock()

	for i := range rf.peers {
		if i == rf.me {
			continue
		}

		go func(server int) {
			rf.mu.Lock()
			if rf.state != Leader || rf.currentTerm != currentTerm {
				rf.mu.Unlock()
				return
			}

			prevLogIndex := rf.nextIndex[server] - 1
			prevLogTerm := 0
			if prevLogIndex >= 0 && prevLogIndex < len(rf.log) {
				prevLogTerm = rf.log[prevLogIndex].Term
			}

			entries := make([]LogEntry, 0)
			if rf.nextIndex[server] < len(rf.log) {
				entries = append(entries, rf.log[rf.nextIndex[server]:]...)
			}

			args := AppendEntriesArgs{
				Term:         currentTerm,
				LeaderId:     rf.me,
				PrevLogIndex: prevLogIndex,
				PrevLogTerm:  prevLogTerm,
				Entries:      entries,
				LeaderCommit: commitIndex,
			}
			rf.mu.Unlock()

			reply := AppendEntriesReply{}
			ok := rf.sendAppendEntries(server, &args, &reply)

			if ok {
				rf.mu.Lock()
				if rf.state == Leader && rf.currentTerm == args.Term {
					if reply.Success {
						rf.matchIndex[server] = args.PrevLogIndex + len(args.Entries)
						rf.nextIndex[server] = rf.matchIndex[server] + 1
					} else {
						if reply.Term > rf.currentTerm {
							rf.currentTerm = reply.Term
							rf.votedFor = -1
							rf.state = Follower
							rf.persist()
						} else {
							if reply.XTerm == -1 {
								rf.nextIndex[server] = reply.XLen
							} else {
								rf.nextIndex[server] = reply.XIndex
							}
							if rf.nextIndex[server] < 0 {
								rf.nextIndex[server] = 0
							}
						}
					}
				}
				rf.mu.Unlock()
			}
		}(i)
	}
}

func (rf *Raft) applyCommitted() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	for rf.lastApplied < rf.commitIndex {
		rf.lastApplied++
		msg := raftapi.ApplyMsg{
			CommandValid: true,
			Command:      rf.log[rf.lastApplied].Command,
			CommandIndex: rf.lastApplied,
		}
		rf.applyCh <- msg
	}
}

func (rf *Raft) updateCommitIndex() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return
	}

	for n := len(rf.log) - 1; n > rf.commitIndex; n-- {
		if rf.log[n].Term != rf.currentTerm {
			continue
		}
		count := 1
		for i := range rf.peers {
			if i != rf.me && rf.matchIndex[i] >= n {
				count++
			}
		}
		if count > len(rf.peers)/2 {
			rf.commitIndex = n
			break
		}
	}
}

func Make(peers []*labrpc.ClientEnd, me int,
	persister *tester.Persister, applyCh chan raftapi.ApplyMsg) raftapi.Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me
	rf.applyCh = applyCh

	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = make([]LogEntry, 1)
	rf.log[0] = LogEntry{Term: 0, Command: nil}
	rf.commitIndex = 0
	rf.lastApplied = 0
	rf.state = Follower
	rf.lastContact = time.Now()

	rf.readPersist(persister.ReadRaftState())

	go rf.ticker()

	go func() {
		for !rf.killed() {
			time.Sleep(100 * time.Millisecond)
			rf.sendHeartbeats()
			rf.updateCommitIndex()
			rf.applyCommitted()
		}
	}()

	return rf
}
