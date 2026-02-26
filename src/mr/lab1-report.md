# MIT 6.5840 Lab 1: MapReduce 实验报告

## 目录
- [实验概述](#实验概述)
- [架构设计](#架构设计)
- [核心实现](#核心实现)
- [面试常见问题](#面试常见问题)
- [测试结果](#测试结果)

---

## 实验概述

本实验实现了一个分布式MapReduce系统，包含：
- **Coordinator**: 负责任务分发、状态管理、容错处理
- **Worker**: 负责执行Map/Reduce任务，通过RPC与Coordinator通信

### MapReduce工作流程
```
输入文件 → Map阶段 → 中间文件 → Reduce阶段 → 输出文件
   ↓           ↓           ↓           ↓           ↓
 pg-*.txt   mr-X-Y     mr-X-Y    mr-out-Z    最终结果
```

---

## 架构设计

### 整体架构图
```
┌─────────────────────────────────────────────────────────┐
│                    Coordinator                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐     │
│  │ Map Tasks   │  │Reduce Tasks │  │  Timeout    │     │
│  │   Queue     │  │   Queue     │  │  Checker    │     │
│  └─────────────┘  └─────────────┘  └─────────────┘     │
│         ↑                ↑                ↑             │
│         └────────────────┴────────────────┘             │
│                       RPC                               │
└─────────────────────────────────────────────────────────┘
         ↑                ↑                ↑
         │                │                │
    ┌────┴────┐      ┌────┴────┐      ┌────┴────┐
    │ Worker1 │      │ Worker2 │      │ Worker3 │
    └─────────┘      └─────────┘      └─────────┘
```

### 任务状态流转
```
     ┌──────────┐
     │   Idle   │ ←─────── 超时重新分配
     └────┬─────┘
          │ 分配任务
          ↓
     ┌──────────┐
     │InProgress│
     └────┬─────┘
          │ 完成任务
          ↓
     ┌──────────┐
     │ Completed│
     └──────────┘
```

---

## 核心实现

### 1. RPC定义 (rpc.go)

```go
// 任务类型
const (
    TaskTypeMap    = "map"
    TaskTypeReduce = "reduce"
    TaskTypeWait   = "wait"    // 等待任务
    TaskTypeDone   = "done"    // 所有任务完成
)

// 任务状态
const (
    TaskStatusIdle       = "idle"
    TaskStatusInProgress = "in_progress"
    TaskStatusCompleted  = "completed"
)
```

### 2. Coordinator核心逻辑 (coordinator.go)

#### 任务分发策略
```go
func (c *Coordinator) GetTask(args *GetTaskArgs, reply *GetTaskReply) error {
    c.mu.Lock()
    defer c.mu.Unlock()

    // 阶段1: 分发Map任务
    if c.phase == "map" {
        taskId := c.findIdleTask(c.mapTasks)
        if taskId != -1 {
            // 分配Map任务
            c.mapTasks[taskId].Status = TaskStatusInProgress
            c.mapTasks[taskId].StartTime = time.Now()
            // ... 返回任务
        }
        // 检查是否所有Map任务完成
        // 完成则切换到reduce阶段
    }

    // 阶段2: 分发Reduce任务
    if c.phase == "reduce" {
        // 类似Map任务分发逻辑
    }
}
```

#### 容错机制 - 超时检测
```go
func (c *Coordinator) checkTimeout() {
    timeout := 10 * time.Second
    now := time.Now()

    // 检查正在执行的任务是否超时
    for i := range c.mapTasks {
        if c.mapTasks[i].Status == TaskStatusInProgress {
            if now.Sub(c.mapTasks[i].StartTime) > timeout {
                c.mapTasks[i].Status = TaskStatusIdle  // 重新分配
            }
        }
    }
}
```

### 3. Worker核心逻辑 (worker.go)

#### Map任务执行
```go
func doMap(task Task, mapf func(string, string) []KeyValue) {
    // 1. 读取输入文件
    content, _ := ioutil.ReadFile(task.InputFile)

    // 2. 执行用户定义的Map函数
    kva := mapf(filename, string(content))

    // 3. 按key分区，写入中间文件
    partitions := make([][]KeyValue, task.NReduce)
    for _, kv := range kva {
        idx := ihash(kv.Key) % task.NReduce
        partitions[idx] = append(partitions[idx], kv)
    }

    // 4. 写入中间文件 mr-MapTaskId-ReduceTaskId
    for i := 0; i < task.NReduce; i++ {
        writeToFile(partitions[i], fmt.Sprintf("mr-%d-%d", task.Id, i))
    }
}
```

#### Reduce任务执行
```go
func doReduce(task Task, reducef func(string, []string) string) {
    // 1. 读取所有Map任务产生的对应分区文件
    for i := 0; i < task.NMap; i++ {
        readFromFile(fmt.Sprintf("mr-%d-%d", i, task.Id))
    }

    // 2. 按Key排序
    sort.Sort(ByKey(kva))

    // 3. 对相同Key的Value分组，执行Reduce函数
    for each distinct key {
        values := collect all values for this key
        output := reducef(key, values)
        write to mr-out-ReduceTaskId
    }
}
```

---

## 面试常见问题

### Q1: MapReduce的工作原理是什么？

**回答要点：**
1. **Map阶段**: 将输入数据分割成独立的数据块，并行处理
2. **Shuffle阶段**: 按Key对中间结果进行分区、排序
3. **Reduce阶段**: 对相同Key的Value进行聚合处理

**本实验体现：**
- Map任务读取输入文件，输出`mr-X-Y`中间文件
- 使用`ihash(key) % NReduce`进行分区
- Reduce任务读取对应分区的所有中间文件，排序后聚合

---

### Q2: 如何处理Worker故障？

**回答要点：**
1. **心跳/超时检测**: Coordinator定期检查任务执行时间
2. **任务重新分配**: 超时任务标记为Idle，重新分配给其他Worker
3. **幂等性保证**: 相同任务多次执行结果一致

**本实验实现：**
```go
// 超时检测 (10秒)
if now.Sub(task.StartTime) > 10*time.Second {
    task.Status = TaskStatusIdle  // 重新分配
}
```

**关键设计：**
- Map任务写入临时文件，完成后重命名为正式文件
- Reduce任务覆盖写入，保证幂等性

---

### Q3: 为什么需要等待所有Map任务完成才能开始Reduce？

**回答要点：**
1. **数据依赖**: Reduce需要读取所有Map任务的输出
2. **分区完整性**: 每个Reduce任务需要处理完整的数据分区
3. **同步屏障**: 确保所有中间数据就绪

**本实验实现：**
```go
// 检查所有Map任务是否完成
allMapDone := true
for _, task := range c.mapTasks {
    if task.Status != TaskStatusCompleted {
        allMapDone = false
        break
    }
}
if allMapDone {
    c.phase = "reduce"  // 切换到Reduce阶段
}
```

---

### Q4: MapReduce如何保证数据局部性？

**回答要点：**
1. **移动计算而非移动数据**: 将计算任务调度到数据所在节点
2. **减少网络传输**: 本地读取输入文件
3. **机架感知调度**: 优先同机架节点

**本实验简化：**
- 单机运行，不涉及网络传输优化
- 实际生产中Hadoop/Spark会考虑数据局部性

---

### Q5: 中间文件的分区策略是什么？为什么？

**回答要点：**
1. **Hash分区**: `hash(key) % NReduce`
2. **负载均衡**: 数据均匀分布到各Reduce任务
3. **确定性**: 相同Key总是分配到同一分区

**本实验实现：**
```go
func ihash(key string) int {
    h := fnv.New32a()
    h.Write([]byte(key))
    return int(h.Sum32() & 0x7fffffff)
}

// 分区
idx := ihash(kv.Key) % task.NReduce
```

---

### Q6: 如果Coordinator故障怎么办？

**回答要点：**
1. **单点故障**: 本实验Coordinator是单点
2. **生产解决方案**:
   - 定期持久化状态到磁盘
   - 使用Raft/Paxos实现高可用
   - 重启后恢复状态

**扩展思考：**
- 本实验简化了Coordinator高可用
- 后续Lab 2会实现Raft共识算法解决这个问题

---

### Q7: MapReduce的适用场景和局限性？

**适用场景：**
- 批处理任务（日志分析、ETL）
- 数据可并行处理
- 对延迟不敏感

**局限性：**
- 不适合迭代计算（每轮都要读写磁盘）
- 不适合流式处理
- 中间结果需要落盘，IO开销大

**改进方案：**
- **Spark**: 内存计算，适合迭代
- **Flink**: 流式处理
- **Dask**: Python生态并行计算

---

### Q8: 如何优化MapReduce性能？

**优化方向：**
1. **Combiner**: Map端预聚合，减少Shuffle数据量
2. **压缩**: 中间数据压缩传输
3. **推测执行**: 慢任务备份执行
4. **小文件合并**: 减少Map任务数量

**本实验可扩展：**
```go
// Combiner示例 (WordCount)
func combiner(key string, values []string) string {
    // Map端预聚合，减少网络传输
    return strconv.Itoa(len(values))
}
```

---

### Q9: 并发安全如何保证？

**回答要点：**
1. **互斥锁**: 保护共享状态
2. **原子操作**: 计数器等简单操作
3. **无锁设计**: 避免锁竞争

**本实验实现：**
```go
type Coordinator struct {
    mu          sync.Mutex  // 保护所有共享状态
    mapTasks    []TaskInfo
    reduceTasks []TaskInfo
    phase       string
    done        bool
}

// 所有访问共享状态的操作都加锁
func (c *Coordinator) GetTask(...) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    // ...
}
```

---

### Q10: RPC通信的设计考量？

**设计要点：**
1. **幂等性**: 同一请求多次执行结果相同
2. **超时重试**: 网络不可靠，需要重试机制
3. **序列化**: 选择高效的序列化格式

**本实验实现：**
```go
// Worker请求任务，失败则重试
for {
    ok := call("Coordinator.GetTask", &args, &reply)
    if !ok {
        time.Sleep(time.Second)
        continue
    }
    // 处理任务...
}
```

---

## 测试结果

```
=== RUN   TestWc
--- PASS: TestWc (12.19s)
=== RUN   TestIndexer
--- PASS: TestIndexer (6.77s)
=== RUN   TestMapParallel
--- PASS: TestMapParallel (8.03s)
=== RUN   TestReduceParallel
--- PASS: TestReduceParallel (10.05s)
=== RUN   TestJobCount
--- PASS: TestJobCount (13.06s)
=== RUN   TestEarlyExit
--- PASS: TestEarlyExit (7.09s)
=== RUN   TestCrashWorker
--- PASS: TestCrashWorker (60.21s)
PASS
ok      6.5840/mr       118.421s
```

### 测试覆盖场景

| 测试用例 | 验证内容 |
|---------|---------|
| TestWc | WordCount正确性 |
| TestIndexer | 文本索引正确性 |
| TestMapParallel | Map任务并行执行 |
| TestReduceParallel | Reduce任务并行执行 |
| TestJobCount | 任务执行次数正确 |
| TestEarlyExit | 进程提前退出处理 |
| TestCrashWorker | Worker崩溃容错 |

---

## 关键代码文件

- [rpc.go](src/mr/rpc.go) - RPC接口定义
- [coordinator.go](src/mr/coordinator.go) - 协调者实现
- [worker.go](src/mr/worker.go) - 工作节点实现

---

## 总结

本实验深入理解了MapReduce的核心原理：
1. **分布式计算模型**: Map-Shuffle-Reduce
2. **容错机制**: 超时检测与任务重试
3. **并发控制**: 互斥锁保护共享状态
4. **RPC通信**: 进程间通信与协调

这些概念是后续分布式系统学习的基础，也是面试中的高频考点。
