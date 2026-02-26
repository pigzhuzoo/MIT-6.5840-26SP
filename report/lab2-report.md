# MIT 6.5840 Lab 2: Key/Value Server 实验报告

## 目录
- [实验概述](#实验概述)
- [架构设计](#架构设计)
- [核心实现](#核心实现)
- [面试常见问题](#面试常见问题)
- [测试结果](#测试结果)

---

## 实验概述

本实验实现了一个线性化的Key/Value服务器，确保：
- **At-most-once语义**: Put操作在网络故障时最多执行一次
- **线性化**: 所有操作看起来像按某种顺序原子执行
- **版本控制**: 通过版本号实现条件更新和重复请求检测

### API设计
```
Get(key) -> (value, version, err)
Put(key, value, version) -> err
```

---

## 架构设计

### 整体架构
```
┌─────────────────────────────────────────────────────┐
│                      Client                          │
│  ┌─────────────────────────────────────────────┐   │
│  │                 Clerk                        │   │
│  │  Get(key) -> (value, version, err)          │   │
│  │  Put(key, value, version) -> err            │   │
│  └─────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
                         │ RPC
                         ▼
┌─────────────────────────────────────────────────────┐
│                    KVServer                          │
│  ┌─────────────────────────────────────────────┐   │
│  │  data: map[string]*KVEntry                   │   │
│  │  KVEntry { Value, Version }                  │   │
│  └─────────────────────────────────────────────┘   │
│                                                     │
│  Get: 返回value和version                           │
│  Put: 条件更新 (version匹配才更新)                  │
└─────────────────────────────────────────────────────┘
```

### 版本号机制
```
初始状态: key不存在

Put(k, v, 0) → 创建key, version=1, 返回OK
Put(k, v, 1) → version匹配, 更新value, version=2, 返回OK
Put(k, v, 1) → version不匹配(当前=2), 返回ErrVersion
Put(k, v, 3) → version不匹配(当前=2), 返回ErrVersion
```

---

## 核心实现

### 1. Server端实现 (server.go)

#### 数据结构
```go
type KVEntry struct {
    Value   string
    Version rpc.Tversion
}

type KVServer struct {
    mu   sync.Mutex
    data map[string]*KVEntry
}
```

#### Get操作
```go
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
```

#### Put操作 - 条件更新
```go
func (kv *KVServer) Put(args *rpc.PutArgs, reply *rpc.PutReply) {
    kv.mu.Lock()
    defer kv.mu.Unlock()

    entry, ok := kv.data[args.Key]

    // 情况1: key不存在
    if !ok {
        if args.Version == 0 {
            // 创建新key
            kv.data[args.Key] = &KVEntry{
                Value:   args.Value,
                Version: 1,
            }
            reply.Err = rpc.OK
        } else {
            // 尝试更新不存在的key
            reply.Err = rpc.ErrNoKey
        }
        return
    }

    // 情况2: version不匹配
    if args.Version != entry.Version {
        reply.Err = rpc.ErrVersion
        return
    }

    // 情况3: version匹配, 执行更新
    entry.Value = args.Value
    entry.Version++
    reply.Err = rpc.OK
}
```

### 2. Client端实现 (client.go)

#### Get操作 - 重试直到成功
```go
func (ck *Clerk) Get(key string) (string, rpc.Tversion, rpc.Err) {
    args := rpc.GetArgs{Key: key}
    reply := rpc.GetReply{}

    for {
        ok := ck.clnt.Call(ck.server, "KVServer.Get", &args, &reply)
        if ok {
            return reply.Value, reply.Version, reply.Err
        }
        // 网络失败, 重试
    }
}
```

#### Put操作 - ErrMaybe处理
```go
func (ck *Clerk) Put(key, value string, version rpc.Tversion) rpc.Err {
    args := rpc.PutArgs{Key: key, Value: value, Version: version}
    reply := rpc.PutReply{}
    first := true

    for {
        ok := ck.clnt.Call(ck.server, "KVServer.Put", &args, &reply)
        if ok {
            if reply.Err == rpc.ErrVersion {
                if first {
                    // 第一次请求就返回ErrVersion
                    // 说明Put肯定没有执行
                    return rpc.ErrVersion
                } else {
                    // 重试后返回ErrVersion
                    // 可能是之前的Put成功了但响应丢失
                    return rpc.ErrMaybe
                }
            }
            return reply.Err
        }
        first = false  // 标记已经重试过
    }
}
```

---

## 面试常见问题

### Q1: 什么是线性化(Linearizability)？

**回答要点：**
1. **定义**: 每个操作看起来在某个时间点原子执行
2. **实时性**: 操作效果在调用和响应之间的某个时刻可见
3. **顺序性**: 后续操作能看到之前操作的效果

**本实验体现：**
- Get操作能看到最近完成的Put操作结果
- 并发操作的结果等同于某种顺序执行的结果
- 使用互斥锁保证单点线性化

---

### Q2: 如何实现At-most-once语义？

**回答要点：**
1. **问题**: 网络故障导致请求重发，可能重复执行
2. **解决方案**:
   - 请求去重（请求ID/序列号）
   - 幂等操作设计
   - 版本号条件更新

**本实验实现：**
```go
// 版本号机制确保条件更新
if args.Version != entry.Version {
    reply.Err = rpc.ErrVersion  // 拒绝过期请求
    return
}
```

**工作原理：**
- 第一次Put(k, v, 1): version匹配，更新成功，version变为2
- 重试Put(k, v, 1): version不匹配(当前=2)，返回ErrVersion
- 客户端收到ErrVersion知道可能已执行，返回ErrMaybe

---

### Q3: ErrVersion和ErrMaybe的区别是什么？

**回答要点：**

| 错误类型 | 含义 | 客户端处理 |
|---------|------|-----------|
| ErrVersion | 版本不匹配 | Put肯定没有执行 |
| ErrMaybe | 可能已执行 | 不确定是否成功 |

**场景分析：**
```
场景1: 第一次请求返回ErrVersion
→ Put肯定没执行（版本号本身就不对）
→ 返回ErrVersion

场景2: 重试请求返回ErrVersion
→ 第一次请求可能成功但响应丢失
→ 版本号已更新，重试时版本不匹配
→ 返回ErrMaybe（不确定是否成功）
```

---

### Q4: 为什么Get操作可以无限重试，而Put需要特殊处理？

**回答要点：**
1. **Get是幂等操作**: 多次执行结果相同
2. **Put不是幂等的**: 条件更新，重试可能导致不同结果

**Get重试安全性：**
```go
// Get只读取数据，不修改状态
// 多次Get返回相同结果（如果没有并发Put）
for {
    ok := ck.clnt.Call(ck.server, "KVServer.Get", &args, &reply)
    if ok {
        return reply.Value, reply.Version, reply.Err
    }
    // 安全重试
}
```

**Put需要区分重试：**
```go
// Put修改状态，需要知道是否是重试
first := true
for {
    ok := ck.clnt.Call(...)
    if ok && reply.Err == rpc.ErrVersion {
        if first {
            return rpc.ErrVersion  // 肯定没执行
        }
        return rpc.ErrMaybe  // 可能已执行
    }
    first = false  // 标记已重试
}
```

---

### Q5: 如何保证并发安全？

**回答要点：**
1. **互斥锁**: 保护共享数据
2. **原子操作**: 整个操作在锁内完成
3. **避免死锁**: 锁的粒度和顺序

**本实验实现：**
```go
func (kv *KVServer) Put(args *rpc.PutArgs, reply *rpc.PutReply) {
    kv.mu.Lock()
    defer kv.mu.Unlock()  // 确保释放锁

    // 整个操作原子执行
    // 读取-检查-更新 不可分割
}
```

**线性化保证：**
- 锁内完成读取和更新
- 其他请求看不到中间状态
- 操作看起来原子执行

---

### Q6: 这个KV Server有什么局限性？

**回答要点：**
1. **单点故障**: 只有一个服务器实例
2. **内存存储**: 重启数据丢失
3. **无持久化**: 没有写入磁盘

**改进方向：**
- **复制**: 使用Raft实现多副本（Lab 2B/3）
- **持久化**: 写入日志/快照
- **分片**: 数据分布到多组服务器（Lab 4）

---

### Q7: 版本号机制如何用于实现锁？

**回答要点：**
版本号可以用于实现分布式锁：

```go
// 加锁
func Lock(key string) error {
    // 尝试写入特定值，version=0表示创建
    err := Put(key, "locked", 0)
    if err == OK {
        return nil  // 获取锁成功
    }
    if err == ErrVersion {
        // 锁已被占用
    }
}

// 解锁
func Unlock(key string) error {
    value, version, err := Get(key)
    if err != nil || value != "locked" {
        return ErrNotLocked
    }
    // 删除锁（用特定version更新为空）
    return Put(key, "", version)
}
```

**关键点：**
- version=0创建新key（获取锁）
- 只有持有正确version才能解锁
- 防止误解锁其他客户端的锁

---

### Q8: 如何处理网络分区？

**回答要点：**
本实验简化了网络分区处理：
- 单服务器，不存在脑裂问题
- 客户端重试直到成功

**生产环境考虑：**
1. **超时机制**: 避免无限等待
2. **多服务器**: 需要共识协议
3. **客户端切换**: 连接其他副本

---

### Q9: 为什么使用版本号而不是时间戳？

**回答要点：**

| 方案 | 优点 | 缺点 |
|-----|------|------|
| 版本号 | 简单、确定性强 | 需要客户端维护 |
| 时间戳 | 不需要维护状态 | 时钟同步问题 |

**版本号优势：**
1. **确定性**: 不依赖时钟同步
2. **简单**: 递增整数，易于比较
3. **明确语义**: 表示修改次数

**时间戳问题：**
- 时钟漂移导致顺序错误
- 需要NTP同步
- 闰秒问题

---

### Q10: 这个实验与后续实验的关系？

**回答要点：**
```
Lab 2A: 单机KV Server (本实验)
   ↓
Lab 2B/3: Raft共识算法
   ↓
Lab 3A: 复制KV Server (基于Raft)
   ↓
Lab 4: 分片KV Server
```

**本实验为后续打基础：**
- 理解线性化概念
- 掌握RPC通信
- 版本号机制用于后续的客户端请求去重

---

## 测试结果

```
=== RUN   TestReliablePut
One client and reliable Put (reliable network)...
  ... Passed --  time  0.0s #peers 1 #RPCs     5 #Ops    5
--- PASS: TestReliablePut (0.14s)

=== RUN   TestPutConcurrentReliable
Test: many clients racing to put values to the same key (reliable network)...
  ... Passed --  time  2.0s #peers 1 #RPCs  2471 #Ops 4942
--- PASS: TestPutConcurrentReliable (2.14s)

=== RUN   TestMemPutManyClientsReliable
Test: memory use many put clients (reliable network)...
  ... Passed --  time 31.8s #peers 1 #RPCs 20000 #Ops 20000
--- PASS: TestMemPutManyClientsReliable (61.61s)

=== RUN   TestUnreliableNet
One client (unreliable network)...
  ... Passed --  time  4.0s #peers 1 #RPCs   243 #Ops  408
--- PASS: TestUnreliableNet (4.15s)

PASS
ok      6.5840/kvsrv1   69.070s
```

### 测试覆盖场景

| 测试用例 | 验证内容 |
|---------|---------|
| TestReliablePut | 基本Put/Get正确性 |
| TestPutConcurrentReliable | 并发Put线性化 |
| TestMemPutManyClientsReliable | 内存使用合理 |
| TestUnreliableNet | 网络容错、ErrMaybe |

---

## 关键代码文件

- [client.go](src/kvsrv1/client.go) - Clerk客户端实现
- [server.go](src/kvsrv1/server.go) - KVServer服务端实现
- [rpc/rpc.go](src/kvsrv1/rpc/rpc.go) - RPC接口定义

---

## 总结

本实验深入理解了分布式系统核心概念：
1. **线性化**: 操作原子执行的假象
2. **At-most-once**: 版本号实现重复请求检测
3. **容错设计**: 重试机制和错误语义
4. **并发控制**: 互斥锁保护共享状态

这些概念是后续Raft和分布式KV实现的基础。
