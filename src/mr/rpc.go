package mr

const (
	TaskTypeMap    = "map"
	TaskTypeReduce = "reduce"
	TaskTypeWait   = "wait"
	TaskTypeDone   = "done"
)

const (
	TaskStatusIdle      = "idle"
	TaskStatusInProgress = "in_progress"
	TaskStatusCompleted  = "completed"
)

type Task struct {
	Type       string
	Id         int
	InputFile  string
	NReduce    int
	NMap       int
}

type GetTaskArgs struct {
}

type GetTaskReply struct {
	Task Task
}

type ReportTaskArgs struct {
	TaskType string
	TaskId   int
}

type ReportTaskReply struct {
	Success bool
}

type ExampleArgs struct {
	X int
}

type ExampleReply struct {
	Y int
}
