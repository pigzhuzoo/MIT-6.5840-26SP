package mr

import "log"
import "net"
import "os"
import "net/rpc"
import "net/http"
import "sync"
import "time"

type TaskInfo struct {
	Status    string
	StartTime time.Time
}

type Coordinator struct {
	mu          sync.Mutex
	files       []string
	nReduce     int
	nMap        int
	mapTasks    []TaskInfo
	reduceTasks []TaskInfo
	phase       string
	done        bool
}

func (c *Coordinator) GetTask(args *GetTaskArgs, reply *GetTaskReply) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase == "map" {
		taskId := c.findIdleTask(c.mapTasks)
		if taskId != -1 {
			c.mapTasks[taskId].Status = TaskStatusInProgress
			c.mapTasks[taskId].StartTime = time.Now()
			reply.Task = Task{
				Type:      TaskTypeMap,
				Id:        taskId,
				InputFile: c.files[taskId],
				NReduce:   c.nReduce,
				NMap:      c.nMap,
			}
			return nil
		}
		for _, task := range c.mapTasks {
			if task.Status == TaskStatusInProgress {
				reply.Task = Task{Type: TaskTypeWait}
				return nil
			}
		}
		c.phase = "reduce"
	}

	if c.phase == "reduce" {
		taskId := c.findIdleTask(c.reduceTasks)
		if taskId != -1 {
			c.reduceTasks[taskId].Status = TaskStatusInProgress
			c.reduceTasks[taskId].StartTime = time.Now()
			reply.Task = Task{
				Type:    TaskTypeReduce,
				Id:      taskId,
				NReduce: c.nReduce,
				NMap:    c.nMap,
			}
			return nil
		}
		for _, task := range c.reduceTasks {
			if task.Status == TaskStatusInProgress {
				reply.Task = Task{Type: TaskTypeWait}
				return nil
			}
		}
		c.done = true
		reply.Task = Task{Type: TaskTypeDone}
		return nil
	}

	reply.Task = Task{Type: TaskTypeWait}
	return nil
}

func (c *Coordinator) findIdleTask(tasks []TaskInfo) int {
	for i, task := range tasks {
		if task.Status == TaskStatusIdle {
			return i
		}
	}
	return -1
}

func (c *Coordinator) ReportTask(args *ReportTaskArgs, reply *ReportTaskReply) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if args.TaskType == TaskTypeMap && c.phase == "map" {
		if c.mapTasks[args.TaskId].Status == TaskStatusInProgress {
			c.mapTasks[args.TaskId].Status = TaskStatusCompleted
		}
	} else if args.TaskType == TaskTypeReduce && c.phase == "reduce" {
		if c.reduceTasks[args.TaskId].Status == TaskStatusInProgress {
			c.reduceTasks[args.TaskId].Status = TaskStatusCompleted
		}
	}
	reply.Success = true
	return nil
}

func (c *Coordinator) checkTimeout() {
	c.mu.Lock()
	defer c.mu.Unlock()

	timeout := 10 * time.Second
	now := time.Now()

	if c.phase == "map" {
		for i := range c.mapTasks {
			if c.mapTasks[i].Status == TaskStatusInProgress {
				if now.Sub(c.mapTasks[i].StartTime) > timeout {
					c.mapTasks[i].Status = TaskStatusIdle
				}
			}
		}
	} else if c.phase == "reduce" {
		for i := range c.reduceTasks {
			if c.reduceTasks[i].Status == TaskStatusInProgress {
				if now.Sub(c.reduceTasks[i].StartTime) > timeout {
					c.reduceTasks[i].Status = TaskStatusIdle
				}
			}
		}
	}
}

func (c *Coordinator) Example(args *ExampleArgs, reply *ExampleReply) error {
	reply.Y = args.X + 1
	return nil
}

func (c *Coordinator) server(sockname string) {
	rpc.Register(c)
	rpc.HandleHTTP()
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatalf("listen error %s: %v", sockname, e)
	}
	go http.Serve(l, nil)
}

func (c *Coordinator) Done() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}

func MakeCoordinator(sockname string, files []string, nReduce int) *Coordinator {
	c := Coordinator{
		files:       files,
		nReduce:     nReduce,
		nMap:        len(files),
		mapTasks:    make([]TaskInfo, len(files)),
		reduceTasks: make([]TaskInfo, nReduce),
		phase:       "map",
		done:        false,
	}

	for i := range c.mapTasks {
		c.mapTasks[i].Status = TaskStatusIdle
	}
	for i := range c.reduceTasks {
		c.reduceTasks[i].Status = TaskStatusIdle
	}

	go func() {
		for {
			time.Sleep(time.Second)
			c.checkTimeout()
		}
	}()

	c.server(sockname)
	return &c
}
