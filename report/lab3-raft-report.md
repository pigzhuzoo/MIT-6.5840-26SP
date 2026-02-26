# MIT 6.5840 Lab 3: Raft 共识算法实验报告

## 目录
- [实验概述](#实验概述)
- [Raft核心概念](#raft核心概念)
- [核心实现](#核心实现)
- [面试常见问题](#面试常见问题)
- [测试结果](#测试结果)

---

## 实验概述

本实验实现了Raft共识算法，这是分布式系统中最重要的共识算法之一。Raft将共识问题分解为三个相对独立的子问题：
1. **领导者选举 (Leader Election)**: 选举出一个领导者
2. **日志复制 (Log Replication)**: 领导者将日志复制到其他服务器
3. **安全性 (Safety)**: 确保所有服务器状态机执行相同的命令序列

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
currentTerm int      // 当前任期号
votedFor    int      // 在当前任期投票给谁
log         []LogEntry // 日志条目
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
        timeout := time.Duration(300+rand.Int63()%150) * time.Millisecond
        elapsed := time.Now().Sub(rf.lastContact)
        
        shouldElection := rf.state != Leader && elapsed > timeout
        rf.mu.Unlock()
        
        if shouldElection {
            rf.startElection()
        }
        time.Sleep(50 * time.Millisecond)
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
    
    // 3. 检查日志一致性
    if args.PrevLogIndex >= len(rf.log) {
        reply.XTerm = -1  // 日志太短
        return
    }
    
    // 4. 日志冲突检测
    if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
        reply.XTerm = rf.log[args.PrevLogIndex].Term
        // 快速回退优化
        return
    }
    
    // 5. 追加新日志
    for i, entry := range args.Entries {
        idx := args.PrevLogIndex + 1 + i
        if idx >= len(rf.log) {
            rf.log = append(rf.log, entry)
        } else if rf.log[idx].Term != entry.Term {
            rf.log = rf.log[:idx]
            rf.log = append(rf.log, entry)
        }
    }
    
    // 6. 更新commitIndex
    if args.LeaderCommit > rf.commitIndex {
        rf.commitIndex = min(args.LeaderCommit, len(rf.log)-1)
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
    rf.persister.Save(w.Bytes(), nil)
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
// 150-300ms的随机超时
timeout := time.Duration(150+rand.Int63()%150) * time.Millisecond
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
    index := len(rf.log)
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

## 测试结果

### 3A: Leader Election
```
=== RUN   TestInitialElection3A
  ... Passed --  time  3.1s #peers 3 #RPCs    62 #Ops    0
=== RUN   TestReElection3A
  ... Passed --  time  5.1s #peers 3 #RPCs   142 #Ops    0
=== RUN   TestManyElections3A
  ... Passed --  time  5.7s #peers 7 #RPCs   557 #Ops    0
PASS
```

### 3B: Log Replication
```
=== RUN   TestBasicAgree3B
  ... Passed --  time  1.1s #peers 3 #RPCs    24 #Ops    3
=== RUN   TestRPCBytes3B
  ... Passed --  time  3.9s #peers 3 #RPCs    80 #Ops   11
=== RUN   TestFollowerFailure3B
  ... Passed --  time  5.0s #peers 3 #RPCs   128 #Ops    3
=== RUN   TestLeaderFailure3B
  ... Passed --  time  5.5s #peers 3 #RPCs   204 #Ops    3
=== RUN   TestFailAgree3B
  ... Passed --  time  6.8s #peers 3 #RPCs   146 #Ops    7
=== RUN   TestFailNoAgree3B
  ... Passed --  time  3.9s #peers 5 #RPCs   216 #Ops    2
=== RUN   TestConcurrentStarts3B
  ... Passed --  time  1.2s #peers 3 #RPCs    22 #Ops    0
=== RUN   TestRejoin3B
  ... Passed --  time  5.1s #peers 3 #RPCs   160 #Ops    4
=== RUN   TestBackup3B
  ... Passed --  time 38.7s #peers 5 #RPCs  3360 #Ops  102
=== RUN   TestCount3B
  ... Passed --  time  2.5s #peers 3 #RPCs    48 #Ops    0
PASS
```

### 3C: Persistence
```
=== RUN   TestPersist13C
  ... Passed --  time  5.5s #peers 3 #RPCs    88 #Ops    6
=== RUN   TestPersist23C
  ... Passed --  time 20.8s #peers 5 #RPCs   538 #Ops   16
=== RUN   TestPersist33C
  ... Passed --  time  3.1s #peers 3 #RPCs    54 #Ops    4
=== RUN   TestFigure83C
  ... Passed --  time 55.5s #peers 5 #RPCs  1295 #Ops    2
=== RUN   TestUnreliableAgree3C
  ... Passed --  time 11.2s #peers 5 #RPCs   424 #Ops  246
=== RUN   TestReliableChurn3C
  ... Passed --  time 17.3s #peers 5 #RPCs   816 #Ops    1
PASS
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

Raft是分布式系统的基石，理解Raft对于理解现代分布式系统（如etcd、TiKV、Consul）至关重要。
