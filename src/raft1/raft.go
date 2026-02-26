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

	snapshotIndex int
	snapshotTerm  int
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
	e.Encode(rf.snapshotIndex)
	e.Encode(rf.snapshotTerm)
	raftstate := w.Bytes()
	rf.persister.Save(raftstate, rf.persister.ReadSnapshot())
}

func (rf *Raft) persistWithSnapshot(snapshot []byte) {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.log)
	e.Encode(rf.snapshotIndex)
	e.Encode(rf.snapshotTerm)
	raftstate := w.Bytes()
	rf.persister.Save(raftstate, snapshot)
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
	var snapshotIndex int
	var snapshotTerm int
	if d.Decode(&currentTerm) != nil ||
		d.Decode(&votedFor) != nil ||
		d.Decode(&log) != nil {
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = log
	}
	if d.Decode(&snapshotIndex) == nil && d.Decode(&snapshotTerm) == nil {
		rf.snapshotIndex = snapshotIndex
		rf.snapshotTerm = snapshotTerm
		rf.lastApplied = snapshotIndex
		rf.commitIndex = snapshotIndex
	}
}

func (rf *Raft) PersistBytes() int {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.persister.RaftStateSize()
}

func (rf *Raft) Snapshot(index int, snapshot []byte) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if index <= rf.snapshotIndex {
		return
	}

	logIdx := index - rf.snapshotIndex
	if logIdx < 0 || logIdx >= len(rf.log) {
		return
	}

	rf.snapshotTerm = rf.log[logIdx].Term
	rf.snapshotIndex = index

	newLog := make([]LogEntry, 0)
	newLog = append(newLog, LogEntry{Term: rf.snapshotTerm, Command: nil})
	for i := logIdx + 1; i < len(rf.log); i++ {
		newLog = append(newLog, rf.log[i])
	}
	rf.log = newLog

	if rf.commitIndex < rf.snapshotIndex {
		rf.commitIndex = rf.snapshotIndex
	}
	if rf.lastApplied < rf.snapshotIndex {
		rf.lastApplied = rf.snapshotIndex
	}

	if rf.state == Leader {
		for i := range rf.peers {
			if i != rf.me && rf.nextIndex[i] < rf.snapshotIndex+1 {
				rf.nextIndex[i] = rf.snapshotIndex + 1
			}
			if rf.matchIndex[i] < rf.snapshotIndex {
				rf.matchIndex[i] = rf.snapshotIndex
			}
		}
	}

	rf.persistWithSnapshot(snapshot)
}

type InstallSnapshotArgs struct {
	Term              int
	LeaderId          int
	LastIncludedIndex int
	LastIncludedTerm  int
	Data              []byte
}

type InstallSnapshotReply struct {
	Term int
}

func (rf *Raft) InstallSnapshot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
	rf.mu.Lock()

	reply.Term = rf.currentTerm

	if args.Term < rf.currentTerm {
		rf.mu.Unlock()
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

	if args.LastIncludedIndex <= rf.snapshotIndex {
		rf.mu.Unlock()
		return
	}

	oldSnapshotIndex := rf.snapshotIndex
	rf.snapshotIndex = args.LastIncludedIndex
	rf.snapshotTerm = args.LastIncludedTerm

	if args.LastIncludedIndex > rf.commitIndex {
		rf.commitIndex = args.LastIncludedIndex
	}
	if args.LastIncludedIndex > rf.lastApplied {
		rf.lastApplied = args.LastIncludedIndex
	}

	newLog := make([]LogEntry, 0)
	newLog = append(newLog, LogEntry{Term: args.LastIncludedTerm, Command: nil})

	logStartIdx := args.LastIncludedIndex + 1 - oldSnapshotIndex
	if logStartIdx >= 0 && logStartIdx < len(rf.log) {
		for i := logStartIdx; i < len(rf.log); i++ {
			newLog = append(newLog, rf.log[i])
		}
	}
	rf.log = newLog

	rf.persistWithSnapshot(args.Data)

	msg := raftapi.ApplyMsg{
		SnapshotValid: true,
		Snapshot:      args.Data,
		SnapshotTerm:  args.LastIncludedTerm,
		SnapshotIndex: args.LastIncludedIndex,
	}
	rf.mu.Unlock()
	rf.applyCh <- msg
}

func (rf *Raft) sendInstallSnapshot(server int, args *InstallSnapshotArgs, reply *InstallSnapshotReply) bool {
	ok := rf.peers[server].Call("Raft.InstallSnapshot", args, reply)
	return ok
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
		rf.persist()
	}

	lastLogIndex := rf.snapshotIndex + len(rf.log) - 1
	lastLogTerm := rf.log[len(rf.log)-1].Term

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

	logIndex := args.PrevLogIndex - rf.snapshotIndex

	if args.PrevLogIndex < rf.snapshotIndex {
		reply.XTerm = -1
		reply.XIndex = -1
		reply.XLen = rf.snapshotIndex + len(rf.log)
		return
	}

	if logIndex >= len(rf.log) {
		reply.XTerm = -1
		reply.XIndex = -1
		reply.XLen = rf.snapshotIndex + len(rf.log)
		return
	}

	if logIndex >= 0 && rf.log[logIndex].Term != args.PrevLogTerm {
		reply.XTerm = rf.log[logIndex].Term
		reply.XIndex = args.PrevLogIndex
		for i := logIndex - 1; i >= 0; i-- {
			if rf.log[i].Term != reply.XTerm {
				reply.XIndex = rf.snapshotIndex + i + 1
				break
			}
		}
		reply.XLen = rf.snapshotIndex + len(rf.log)
		return
	}

	reply.Success = true

	for i, entry := range args.Entries {
		idx := logIndex + 1 + i
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

	index := rf.snapshotIndex + len(rf.log)
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
		timeout := time.Duration(300+rand.Int63()%200) * time.Millisecond
		elapsed := now.Sub(rf.lastContact)

		shouldElection := rf.state != Leader && elapsed > timeout
		rf.mu.Unlock()

		if shouldElection {
			rf.startElection()
		}

		ms := 30 + (rand.Int63() % 30)
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

	lastLogIndex := rf.snapshotIndex + len(rf.log) - 1
	lastLogTerm := rf.log[len(rf.log)-1].Term

	args := RequestVoteArgs{
		Term:         currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
	}
	rf.mu.Unlock()

	votes := int32(1)
	electionWon := int32(0)

	for i := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			reply := RequestVoteReply{}
			ok := rf.sendRequestVote(server, &args, &reply)
			if ok {
				rf.mu.Lock()
				if rf.state == Candidate && rf.currentTerm == args.Term {
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.votedFor = -1
						rf.state = Follower
						rf.persist()
					} else if reply.VoteGranted {
						atomic.AddInt32(&votes, 1)
						if atomic.LoadInt32(&votes) > int32(len(rf.peers)/2) && atomic.CompareAndSwapInt32(&electionWon, 0, 1) {
							rf.state = Leader
							rf.nextIndex = make([]int, len(rf.peers))
							rf.matchIndex = make([]int, len(rf.peers))
							for j := range rf.nextIndex {
								rf.nextIndex[j] = rf.snapshotIndex + len(rf.log)
								rf.matchIndex[j] = rf.snapshotIndex
							}
							rf.matchIndex[rf.me] = rf.snapshotIndex + len(rf.log) - 1
						}
					}
				}
				rf.mu.Unlock()
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
	snapshotIndex := rf.snapshotIndex
	snapshot := rf.persister.ReadSnapshot()

	serversToSend := make([]int, 0)
	for i := range rf.peers {
		if i != rf.me {
			serversToSend = append(serversToSend, i)
		}
	}
	rf.mu.Unlock()

	for _, server := range serversToSend {
		go func(srv int) {
			rf.mu.Lock()
			if rf.state != Leader || rf.currentTerm != currentTerm {
				rf.mu.Unlock()
				return
			}

			if rf.nextIndex[srv] <= snapshotIndex && len(snapshot) > 0 {
				args := InstallSnapshotArgs{
					Term:              currentTerm,
					LeaderId:          rf.me,
					LastIncludedIndex: snapshotIndex,
					LastIncludedTerm:  rf.snapshotTerm,
					Data:              snapshot,
				}
				rf.mu.Unlock()

				reply := InstallSnapshotReply{}
				ok := rf.sendInstallSnapshot(srv, &args, &reply)
				if ok {
					rf.mu.Lock()
					if rf.state == Leader && rf.currentTerm == args.Term {
						if reply.Term > rf.currentTerm {
							rf.currentTerm = reply.Term
							rf.votedFor = -1
							rf.state = Follower
							rf.persist()
						} else {
							rf.matchIndex[srv] = args.LastIncludedIndex
							rf.nextIndex[srv] = rf.matchIndex[srv] + 1
						}
					}
					rf.mu.Unlock()
				}
				return
			}

			prevLogIndex := rf.nextIndex[srv] - 1
			prevLogTerm := 0
			logIdx := prevLogIndex - rf.snapshotIndex
			if logIdx >= 0 && logIdx < len(rf.log) {
				prevLogTerm = rf.log[logIdx].Term
			}

			entries := make([]LogEntry, 0)
			startIdx := rf.nextIndex[srv] - rf.snapshotIndex
			if startIdx >= 0 && startIdx < len(rf.log) {
				entries = append(entries, rf.log[startIdx:]...)
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
			ok := rf.sendAppendEntries(srv, &args, &reply)

			if ok {
				rf.mu.Lock()
				if rf.state == Leader && rf.currentTerm == args.Term {
					if reply.Success {
						newMatch := args.PrevLogIndex + len(args.Entries)
						if newMatch > rf.matchIndex[srv] {
							rf.matchIndex[srv] = newMatch
							rf.nextIndex[srv] = rf.matchIndex[srv] + 1
						}
					} else {
						if reply.Term > rf.currentTerm {
							rf.currentTerm = reply.Term
							rf.votedFor = -1
							rf.state = Follower
							rf.persist()
						} else {
							if reply.XTerm == -1 {
								rf.nextIndex[srv] = reply.XLen
							} else {
								rf.nextIndex[srv] = reply.XIndex
							}
							if rf.nextIndex[srv] < 1 {
								rf.nextIndex[srv] = 1
							}
						}
					}
				}
				rf.mu.Unlock()
			}
		}(server)
	}
}

func (rf *Raft) applyCommitted() {
	var msgs []raftapi.ApplyMsg
	rf.mu.Lock()
	for rf.lastApplied < rf.commitIndex {
		rf.lastApplied++
		logIdx := rf.lastApplied - rf.snapshotIndex
		if logIdx >= 0 && logIdx < len(rf.log) {
			msg := raftapi.ApplyMsg{
				CommandValid: true,
				Command:      rf.log[logIdx].Command,
				CommandIndex: rf.lastApplied,
			}
			msgs = append(msgs, msg)
		}
	}
	rf.mu.Unlock()
	for _, msg := range msgs {
		rf.applyCh <- msg
	}
}

func (rf *Raft) updateCommitIndex() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return
	}

	for n := rf.snapshotIndex + len(rf.log) - 1; n > rf.commitIndex; n-- {
		logIdx := n - rf.snapshotIndex
		if logIdx < 0 || logIdx >= len(rf.log) {
			continue
		}
		if rf.log[logIdx].Term != rf.currentTerm {
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
	rf.snapshotIndex = 0
	rf.snapshotTerm = 0
	rf.state = Follower
	rf.lastContact = time.Now()

	rf.readPersist(persister.ReadRaftState())

	go rf.ticker()

	go func() {
		for !rf.killed() {
			time.Sleep(80 * time.Millisecond)
			rf.sendHeartbeats()
			rf.updateCommitIndex()
			rf.applyCommitted()
		}
	}()

	return rf
}
