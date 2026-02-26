# MIT 6.5840 Lab 3: Raft 共识算法实验报告

## 目录
- [实验概述](#实验概述)
- [Raft核心概念](#raft核心概念)
- [核心实现](#核心实现)
- [3D: Snapshot快照机制](#3d-snapshot快照机制)
- [面试常见问题](#面试常见问题)
- [测试结果](#测试结果)

---

## 实验概述

本实验实现了Raft共识算法，这是分布式系统中最重要的共识算法之一。Raft将共识问题分解为三个相对独立的子问题：
1. **领导者选举 (Leader Election)**: 选举出一个领导者
2. **日志复制 (Log Replication)**: 领导者将日志复制到其他服务器
3. **安全性 (Safety)**: 确保所有服务器状态机执行相同的命令序列
4. **快照 (Snapshot)**: 压缩日志，防止日志无限增长

### Raft节点状态
```
┌─────────────┐     选举超时      ┌─────────────┐
│  Follower   │ ────────────────> │  Candidate  │
└─────────────┘                   └─────────────┘
       ^                                 │
       │                                 │ 获得多数票
       │ 发现更高term                     │
       │                                 ↓
       │                          ┌─────────────┐
       └──────────────────────────│   Leader    │
                                  └─────────────┘
```

---

## Raft核心概念

### 1. Term (任期)
- Raft将时间划分为任意长度的任期
- 每个任期从选举开始
- 如果选举失败（平票），任期结束但没有领导者
- 任期号单调递增

### 2. 持久化状态
```go
currentTerm   int        // 当前任期号
votedFor      int        // 在当前任期投票给谁
log           []LogEntry // 日志条目
snapshotIndex int        // 快照包含的最后日志索引
snapshotTerm  int        // 快照包含的最后日志任期
```

### 3. 易失性状态
```go
commitIndex int      // 已知已提交的最高日志索引
lastApplied int      // 最后应用到状态机的日志索引

// Leader特有
nextIndex  []int     // 对每个服务器，下一个发送的日志索引
matchIndex []int     // 对每个服务器，已复制的最高日志索引
```

---

## 核心实现

### 1. 领导者选举 (3A)

#### 选举超时检测
```go
func (rf *Raft) ticker() {
    for !rf.killed() {
        rf.mu.Lock()
        timeout := time.Duration(300+rand.Int63()%200) * time.Millisecond
        elapsed := time.Now().Sub(rf.lastContact)
        
        shouldElection := rf.state != Leader && elapsed > timeout
        rf.mu.Unlock()
        
        if shouldElection {
            rf.startElection()
        }
        time.Sleep(30 * time.Millisecond)
    }
}
```

#### 选举流程
```go
func (rf *Raft) startElection() {
    // 1. 转为Candidate，增加term
    rf.state = Candidate
    rf.currentTerm++
    rf.votedFor = rf.me
    
    // 2. 并行发送RequestVote RPC
    for i := range rf.peers {
        go func(server int) {
            reply := RequestVoteReply{}
            if rf.sendRequestVote(server, &args, &reply) {
                // 3. 统计票数
                if reply.VoteGranted {
                    votes++
                    if votes > len(rf.peers)/2 {
                        rf.state = Leader
                    }
                }
            }
        }(i)
    }
}
```

### 2. 日志复制 (3B)

#### AppendEntries RPC处理
```go
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
    // 1. 拒绝过期的term
    if args.Term < rf.currentTerm {
        return
    }
    
    // 2. 重置选举超时
    rf.lastContact = time.Now()
    rf.state = Follower
    
    // 3. 检查日志一致性（考虑snapshot）
    logIndex := args.PrevLogIndex - rf.snapshotIndex
    
    if args.PrevLogIndex < rf.snapshotIndex {
        // Leader的prevLogIndex已经被本节点snapshot了
        reply.XTerm = -1
        return
    }
    
    if logIndex >= len(rf.log) {
        reply.XTerm = -1  // 日志太短
        return
    }
    
    // 4. 日志冲突检测
    if rf.log[logIndex].Term != args.PrevLogTerm {
        reply.XTerm = rf.log[logIndex].Term
        // 快速回退优化
        return
    }
    
    // 5. 追加新日志
    for i, entry := range args.Entries {
        idx := logIndex + 1 + i
        if idx >= len(rf.log) {
            rf.log = append(rf.log, entry)
        } else if rf.log[idx].Term != entry.Term {
            rf.log = rf.log[:idx]
            rf.log = append(rf.log, entry)
        }
    }
    
    // 6. 更新commitIndex
    if args.LeaderCommit > rf.commitIndex {
        rf.commitIndex = min(args.LeaderCommit, rf.snapshotIndex + len(rf.log) - 1)
    }
}
```

#### 快速回退优化
```go
// Follower返回冲突信息
reply.XTerm = rf.log[args.PrevLogIndex].Term  // 冲突term
reply.XIndex = 找到该term的第一个索引

// Leader根据回退
if reply.XTerm == -1 {
    rf.nextIndex[server] = reply.XLen  // 日志太短
} else {
    rf.nextIndex[server] = reply.XIndex  // 跳过冲突term
}
```

### 3. 持久化 (3C)

```go
func (rf *Raft) persist() {
    w := new(bytes.Buffer)
    e := labgob.NewEncoder(w)
    e.Encode(rf.currentTerm)
    e.Encode(rf.votedFor)
    e.Encode(rf.log)
    e.Encode(rf.snapshotIndex)
    e.Encode(rf.snapshotTerm)
    rf.persister.Save(w.Bytes(), rf.persister.ReadSnapshot())
}

func (rf *Raft) readPersist(data []byte) {
    if data == nil || len(data) < 1 {
        return
    }
    r := bytes.NewBuffer(data)
    d := labgob.NewDecoder(r)
    d.Decode(&rf.currentTerm)
    d.Decode(&rf.votedFor)
    d.Decode(&rf.log)
    // 恢复snapshot状态
    if d.Decode(&snapshotIndex) == nil && d.Decode(&snapshotTerm) == nil {
        rf.snapshotIndex = snapshotIndex
        rf.snapshotTerm = snapshotTerm
        rf.lastApplied = snapshotIndex  // 关键：恢复lastApplied
        rf.commitIndex = snapshotIndex
    }
}
```

---

## 3D: Snapshot快照机制

### 为什么需要快照？

在实际系统中，日志会无限增长，导致：
1. **存储压力**: 日志占用大量磁盘空间
2. **重启恢复慢**: 需要重放大量日志
3. **新节点同步慢**: 需要传输大量日志

快照机制通过定期将状态机状态保存到快照，并丢弃之前的日志来解决这些问题。

### 快照数据结构

```go
type InstallSnapshotArgs struct {
    Term              int
    LeaderId          int
    LastIncludedIndex int     // 快照包含的最后日志索引
    LastIncludedTerm  int     // 快照包含的最后日志任期
    Data              []byte  // 快照数据
}
```

### 关键实现

#### 1. 创建快照 (Snapshot)

```go
func (rf *Raft) Snapshot(index int, snapshot []byte) {
    rf.mu.Lock()
    defer rf.mu.Unlock()

    // 边界检查
    if index <= rf.snapshotIndex {
        return
    }

    logIdx := index - rf.snapshotIndex
    if logIdx < 0 || logIdx >= len(rf.log) {
        return
    }

    // 更新snapshot元数据
    rf.snapshotTerm = rf.log[logIdx].Term
    rf.snapshotIndex = index

    // 裁剪日志，保留snapshot之后的条目
    newLog := make([]LogEntry, 0)
    newLog = append(newLog, LogEntry{Term: rf.snapshotTerm, Command: nil})
    for i := logIdx + 1; i < len(rf.log); i++ {
        newLog = append(newLog, rf.log[i])
    }
    rf.log = newLog

    // 更新commitIndex和lastApplied
    if rf.commitIndex < rf.snapshotIndex {
        rf.commitIndex = rf.snapshotIndex
    }
    if rf.lastApplied < rf.snapshotIndex {
        rf.lastApplied = rf.snapshotIndex
    }

    // Leader需要更新nextIndex和matchIndex
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
```

#### 2. 安装快照 (InstallSnapshot RPC)

```go
func (rf *Raft) InstallSnapshot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
    rf.mu.Lock()

    reply.Term = rf.currentTerm

    // 拒绝过期term
    if args.Term < rf.currentTerm {
        rf.mu.Unlock()
        return
    }

    // 更新term和状态
    if args.Term > rf.currentTerm {
        rf.currentTerm = args.Term
        rf.votedFor = -1
        rf.state = Follower
        rf.persist()
    }

    rf.lastContact = time.Now()
    rf.state = Follower

    // 快照已经是旧的了
    if args.LastIncludedIndex <= rf.snapshotIndex {
        rf.mu.Unlock()
        return
    }

    // 更新snapshot元数据
    oldSnapshotIndex := rf.snapshotIndex
    rf.snapshotIndex = args.LastIncludedIndex
    rf.snapshotTerm = args.LastIncludedTerm

    // 更新commitIndex和lastApplied
    if args.LastIncludedIndex > rf.commitIndex {
        rf.commitIndex = args.LastIncludedIndex
    }
    if args.LastIncludedIndex > rf.lastApplied {
        rf.lastApplied = args.LastIncludedIndex
    }

    // 保留snapshot之后的日志条目
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

    // 发送快照到applyCh（必须在释放锁之后）
    msg := raftapi.ApplyMsg{
        SnapshotValid: true,
        Snapshot:      args.Data,
        SnapshotTerm:  args.LastIncludedTerm,
        SnapshotIndex: args.LastIncludedIndex,
    }
    rf.mu.Unlock()
    rf.applyCh <- msg  // 关键：释放锁后再发送
}
```

#### 3. Leader发送快照

```go
func (rf *Raft) sendHeartbeats() {
    // ... 获取snapshot等数据 ...
    
    for _, server := range serversToSend {
        go func(srv int) {
            rf.mu.Lock()
            
            // 如果follower落后于snapshot，发送InstallSnapshot
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
            
            // 否则发送AppendEntries
            // ...
        }(server)
    }
}
```

### 关键问题与解决方案

#### 问题1: 死锁问题
**现象**: 测试卡住不输出任何信息

**原因**: 在持有锁时向`applyCh`发送消息，而消费者可能需要获取锁来处理消息

**解决方案**: 先释放锁，再发送消息
```go
rf.mu.Unlock()
rf.applyCh <- msg  // 在锁外发送
```

#### 问题2: 日志索引计算错误
**现象**: InstallSnapshot后日志丢失或重复

**原因**: `logStartIdx`计算错误

**错误代码**:
```go
logStartIdx := args.LastIncludedIndex - oldSnapshotIndex
```

**正确代码**:
```go
logStartIdx := args.LastIncludedIndex + 1 - oldSnapshotIndex
```

#### 问题3: 重启后apply out of order
**现象**: `server X apply out of order, expected index Y, got Z`

**原因**: 从持久化状态恢复时，`lastApplied`没有正确初始化

**解决方案**: 在`readPersist`中初始化`lastApplied`和`commitIndex`
```go
if d.Decode(&snapshotIndex) == nil && d.Decode(&snapshotTerm) == nil {
    rf.snapshotIndex = snapshotIndex
    rf.snapshotTerm = snapshotTerm
    rf.lastApplied = snapshotIndex  // 关键修复
    rf.commitIndex = snapshotIndex
}
```

#### 问题4: 非Leader访问nextIndex/matchIndex
**现象**: 空指针panic

**原因**: 只有Leader才初始化`nextIndex`和`matchIndex`

**解决方案**: 在`Snapshot`中添加状态检查
```go
if rf.state == Leader {
    // 更新nextIndex和matchIndex
}
```

---

## 面试常见问题

### Q1: Raft如何保证安全性（Leader完整性）？

**回答要点：**
1. **选举限制**: Candidate必须拥有最新日志才能当选
2. **投票检查**: 只有日志至少一样新的Candidate才能获得投票

**代码实现：**
```go
// RequestVote处理
upToDate := args.LastLogTerm > lastLogTerm ||
    (args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex)

if !upToDate {
    reply.VoteGranted = false
}
```

**为什么有效：**
- 如果Candidate日志落后，无法获得多数票
- 新Leader必须包含所有已提交的日志

---

### Q2: Raft如何处理网络分区？

**回答要点：**
1. **多数派原则**: 只有获得多数票才能成为Leader
2. **Term机制**: 更高Term的Leader会使旧Leader降级

**场景分析：**
```
分区前: [A Leader] [B Follower] [C Follower]

分区后:
分区1: [A Leader] (无法获得多数，无法提交)
分区2: [B] [C] → 选举新Leader

分区恢复后:
- B/C的term更高
- A收到更高term的心跳，转为Follower
```

---

### Q3: 为什么日志索引从1开始？

**回答要点：**
1. **简化边界处理**: 索引0作为dummy entry
2. **PrevLogIndex=-1**: 表示空日志的情况

**实现：**
```go
// 初始化时添加dummy entry
rf.log = make([]LogEntry, 1)
rf.log[0] = LogEntry{Term: 0, Command: nil}
rf.commitIndex = 0
rf.lastApplied = 0
```

---

### Q4: Raft的选举超时为什么是随机的？

**回答要点：**
1. **避免活锁**: 防止多个节点同时发起选举
2. **快速恢复**: 确保选举能在合理时间内完成

**实现：**
```go
// 300-500ms的随机超时
timeout := time.Duration(300+rand.Int63()%200) * time.Millisecond
```

**为什么这个范围：**
- 太短：频繁选举，系统不稳定
- 太长：故障检测慢，可用性降低

---

### Q5: Raft vs Paxos的区别？

| 方面 | Raft | Paxos |
|------|------|-------|
| 理解难度 | 简单，模块化 | 复杂，难以理解 |
| Leader | 强Leader | 无固定Leader |
| 日志顺序 | 连续，有序 | 可能不连续 |
| 实现难度 | 相对容易 | 较难正确实现 |

**Raft优势：**
- 清晰的模块划分
- 易于理解和实现
- 工业界广泛使用（etcd, Consul, TiKV）

---

### Q6: 什么是Split Vote？如何解决？

**回答要点：**
1. **定义**: 多个Candidate同时获得相同票数
2. **解决**: 随机选举超时，让某个Candidate先超时

**场景：**
```
3节点集群，2个Candidate同时发起选举
A: 投票给自己 (1票)
B: 投票给自己 (1票)
C: 可能投给A或B

如果C没投票或平票，需要新一轮选举
随机超时确保下一轮只有一个先超时
```

---

### Q7: 如何保证日志的一致性？

**回答要点：**
1. **日志匹配特性**: 
   - 如果两个日志在相同索引有相同term，则之前所有日志相同
   - 如果某个日志条目在某个term被提交，则该条目在所有更高term日志中存在

2. **一致性检查**:
```go
// Leader发送PrevLogIndex和PrevLogTerm
// Follower检查是否匹配
if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
    // 不匹配，拒绝
}
```

---

### Q8: Raft如何实现线性一致性？

**回答要点：**
1. **Leader串行处理**: 所有请求通过Leader
2. **日志顺序**: 按日志索引顺序应用
3. **提交后才响应**: 确保多数派确认后才返回

**实现：**
```go
func (rf *Raft) Start(command interface{}) (int, int, bool) {
    if rf.state != Leader {
        return -1, rf.currentTerm, false
    }
    index := rf.snapshotIndex + len(rf.log)
    rf.log = append(rf.log, LogEntry{Term: rf.currentTerm, Command: command})
    return index, rf.currentTerm, true
}

// 等待commitIndex >= index后才应用
```

---

### Q9: 为什么需要持久化？哪些状态需要持久化？

**回答要点：**
1. **必要性**: 节点崩溃后恢复，保证安全性
2. **需要持久化的状态**:
   - currentTerm: 防止一个term投多票
   - votedFor: 防止一个term投多票
   - log: 保证日志不丢失
   - snapshotIndex, snapshotTerm: 快照元数据

3. **不需要持久化的状态**:
   - commitIndex, lastApplied: 可通过日志重建
   - nextIndex, matchIndex: Leader特有，重启后重新初始化

---

### Q10: Raft的性能瓶颈在哪里？

**回答要点：**
1. **Leader瓶颈**: 所有请求经过Leader
2. **磁盘IO**: 持久化日志
3. **网络延迟**: 复制到多数派

**优化方向：**
- **批量提交**: 减少RPC次数
- **异步持久化**: 先响应再持久化（牺牲部分安全性）
- **ReadIndex**: 读请求不需要写日志
- **Lease Read**: Leader租约，避免读请求走共识

---

### Q11: 快照机制如何保证一致性？

**回答要点：**
1. **Leader负责**: 只有Leader才能发起InstallSnapshot
2. **Term检查**: 拒绝过期term的快照
3. **日志保留**: 快照之后的日志需要保留

**关键点：**
- 快照索引之前的日志可以被丢弃
- 快照索引之后的所有日志必须保留
- `lastApplied`必须正确初始化为`snapshotIndex`

---

### Q12: 什么情况下需要发送InstallSnapshot？

**回答要点：**
当Follower的`nextIndex`小于等于Leader的`snapshotIndex`时，说明Follower需要的日志已经被Leader快照裁剪掉了，需要通过InstallSnapshot来同步。

**判断条件：**
```go
if rf.nextIndex[srv] <= snapshotIndex && len(snapshot) > 0 {
    // 发送InstallSnapshot
}
```

---

## 测试结果

### 3A: Leader Election
```
=== RUN   TestInitialElection3A
Test (3A): initial election (reliable network)...
  ... Passed --  time  3.0s #peers 3 #RPCs    74 #Ops    0
--- PASS: TestInitialElection3A (3.37s)
=== RUN   TestReElection3A
Test (3A): election after network failure (reliable network)...
  ... Passed --  time  4.6s #peers 3 #RPCs   146 #Ops    0
--- PASS: TestReElection3A (5.01s)
=== RUN   TestManyElections3A
Test (3A): multiple elections (reliable network)...
  ... Passed --  time  5.4s #peers 7 #RPCs   732 #Ops    0
--- PASS: TestManyElections3A (6.65s)
PASS
ok      6.5840/raft1    15.038s
```

### 3B: Log Replication
```
=== RUN   TestBasicAgree3B
Test (3B): basic agreement (reliable network)...
  ... Passed --  time  1.0s #peers 3 #RPCs    22 #Ops    3
=== RUN   TestRPCBytes3B
Test (3B): RPC byte count (reliable network)...
  ... Passed --  time  3.0s #peers 3 #RPCs    72 #Ops   11
=== RUN   TestFollowerFailure3B
Test (3B): test progressive failure of followers (reliable network)...
  ... Passed --  time  4.9s #peers 3 #RPCs   150 #Ops    3
=== RUN   TestLeaderFailure3B
Test (3B): test failure of leaders (reliable network)...
  ... Passed --  time  5.2s #peers 3 #RPCs   242 #Ops    3
=== RUN   TestFailAgree3B
Test (3B): agreement after follower reconnects (reliable network)...
  ... Passed --  time  6.3s #peers 3 #RPCs   166 #Ops    7
=== RUN   TestFailNoAgree3B
Test (3B): no agreement if too many followers disconnect (reliable network)...
  ... Passed --  time  3.8s #peers 5 #RPCs   252 #Ops    2
=== RUN   TestConcurrentStarts3B
Test (3B): concurrent Start()s (reliable network)...
  ... Passed --  time  0.8s #peers 3 #RPCs    14 #Ops    0
=== RUN   TestRejoin3B
Test (3B): rejoin of partitioned leader (reliable network)...
  ... Passed --  time  4.6s #peers 3 #RPCs   188 #Ops    4
=== RUN   TestBackup3B
Test (3B): leader backs up quickly over incorrect follower logs (reliable network)...
  ... Passed --  time 29.8s #peers 5 #RPCs  3164 #Ops  102
=== RUN   TestCount3B
Test (3B): RPC counts aren't too high (reliable network)...
  ... Passed --  time  2.3s #peers 3 #RPCs    56 #Ops    0
PASS
ok      6.5840/raft1    66.820s
```

### 3C: Persistence
```
=== RUN   TestPersist13C
Test (3C): basic persistence (reliable network)...
  ... Passed --  time  5.3s #peers 3 #RPCs   102 #Ops    6
=== RUN   TestPersist23C
Test (3C): more persistence (reliable network)...
  ... Passed --  time 18.8s #peers 5 #RPCs   579 #Ops   16
=== RUN   TestPersist33C
Test (3C): partitioned leader and one follower crash, leader restarts (reliable network)...
  ... Passed --  time  2.6s #peers 3 #RPCs    54 #Ops    4
=== RUN   TestFigure83C
Test (3C): Figure 8 (reliable network)...
  ... Passed --  time 49.0s #peers 5 #RPCs  1260 #Ops    2
=== RUN   TestUnreliableAgree3C
Test (3C): unreliable agreement (unreliable network)...
  ... Passed --  time  8.8s #peers 5 #RPCs   428 #Ops  246
=== RUN   TestFigure8Unreliable3C
Test (3C): Figure 8 (unreliable) (unreliable network)...
  ... Passed --  time 41.9s #peers 5 #RPCs  4072 #Ops    2
=== RUN   TestReliableChurn3C
Test (3C): churn (reliable network)...
  ... Passed --  time 16.8s #peers 5 #RPCs   900 #Ops    1
=== RUN   TestUnreliableChurn3C
Test (3C): unreliable churn (unreliable network)...
  ... Passed --  time 17.7s #peers 5 #RPCs   624 #Ops    1
PASS
ok      6.5840/raft1    166.540s
```

### 3D: Snapshot
```
=== RUN   TestSnapshotBasic3D
Test (3D): snapshots basic (reliable network)...
  ... Passed --  time 10.0s #peers 3 #RPCs   238 #Ops   31
=== RUN   TestSnapshotInstall3D
Test (3D): install snapshots (disconnect) (reliable network)...
  ... Passed --  time 98.1s #peers 3 #RPCs  2657 #Ops   91
=== RUN   TestSnapshotInstallUnreliable3D
Test (3D): install snapshots (disconnect) (unreliable network)...
  ... Passed --  time 100.5s #peers 3 #RPCs  2802 #Ops   91
=== RUN   TestSnapshotInstallCrash3D
Test (3D): install snapshots (crash) (reliable network)...
  ... Passed --  time 50.0s #peers 3 #RPCs  1160 #Ops   91
=== RUN   TestSnapshotInstallUnCrash3D
Test (3D): install snapshots (crash) (unreliable network)...
  ... Passed --  time 60.3s #peers 3 #RPCs  1360 #Ops   91
=== RUN   TestSnapshotAllCrash3D
Test (3D): crash and restart all servers (unreliable network)...
  ... Passed --  time 20.2s #peers 3 #RPCs   440 #Ops   56
=== RUN   TestSnapshotInit3D
Test (3D): snapshot initialization after crash (unreliable network)...
  ... Passed --  time  5.6s #peers 3 #RPCs   111 #Ops   14
PASS
ok      6.5840/raft1    347.981s
```

---

## 关键代码文件

- [raft.go](../src/raft1/raft.go) - Raft核心实现

---

## 总结

本实验深入理解了Raft共识算法的核心原理：
1. **领导者选举**: 随机超时 + 多数投票
2. **日志复制**: AppendEntries RPC + 一致性检查
3. **持久化**: 保证崩溃恢复后的安全性
4. **快照机制**: 压缩日志，支持落后节点同步

### 关键经验教训

1. **避免死锁**: 不要在持有锁时向channel发送消息
2. **索引计算**: 快照场景下日志索引需要仔细计算
3. **状态恢复**: 从持久化状态恢复时，所有相关状态都需要正确初始化
4. **边界条件**: Leader/Follower状态检查、空日志处理等边界条件

Raft是分布式系统的基石，理解Raft对于理解现代分布式系统（如etcd、TiKV、Consul）至关重要。
