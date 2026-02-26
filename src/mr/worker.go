package mr

import "fmt"
import "log"
import "net/rpc"
import "hash/fnv"
import "os"
import "io/ioutil"
import "sort"
import "time"
import "encoding/json"

type KeyValue struct {
	Key   string
	Value string
}

func ihash(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() & 0x7fffffff)
}

var coordSockName string

func Worker(sockname string, mapf func(string, string) []KeyValue,
	reducef func(string, []string) string) {

	coordSockName = sockname

	for {
		args := GetTaskArgs{}
		reply := GetTaskReply{}
		ok := call("Coordinator.GetTask", &args, &reply)
		if !ok {
			time.Sleep(time.Second)
			continue
		}

		task := reply.Task

		switch task.Type {
		case TaskTypeMap:
			doMap(task, mapf)
			reportTask(TaskTypeMap, task.Id)
		case TaskTypeReduce:
			doReduce(task, reducef)
			reportTask(TaskTypeReduce, task.Id)
		case TaskTypeWait:
			time.Sleep(time.Second)
		case TaskTypeDone:
			return
		}
	}
}

func doMap(task Task, mapf func(string, string) []KeyValue) {
	filename := task.InputFile
	file, err := os.Open(filename)
	if err != nil {
		log.Fatalf("cannot open %v", filename)
	}
	content, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalf("cannot read %v", filename)
	}
	file.Close()

	kva := mapf(filename, string(content))

	partitions := make([][]KeyValue, task.NReduce)
	for _, kv := range kva {
		idx := ihash(kv.Key) % task.NReduce
		partitions[idx] = append(partitions[idx], kv)
	}

	for i := 0; i < task.NReduce; i++ {
		oname := intermediateName(task.Id, i)
		ofile, err := os.Create(oname)
		if err != nil {
			log.Fatalf("cannot create %v", oname)
		}
		enc := json.NewEncoder(ofile)
		for _, kv := range partitions[i] {
			err := enc.Encode(&kv)
			if err != nil {
				log.Fatalf("cannot encode %v", kv)
			}
		}
		ofile.Close()
	}
}

func doReduce(task Task, reducef func(string, []string) string) {
	var kva []KeyValue

	for i := 0; i < task.NMap; i++ {
		iname := intermediateName(i, task.Id)
		file, err := os.Open(iname)
		if err != nil {
			continue
		}
		dec := json.NewDecoder(file)
		for {
			var kv KeyValue
			if err := dec.Decode(&kv); err != nil {
				break
			}
			kva = append(kva, kv)
		}
		file.Close()
	}

	sort.Sort(ByKey(kva))

	oname := fmt.Sprintf("mr-out-%d", task.Id)
	ofile, err := os.Create(oname)
	if err != nil {
		log.Fatalf("cannot create %v", oname)
	}

	i := 0
	for i < len(kva) {
		j := i + 1
		for j < len(kva) && kva[j].Key == kva[i].Key {
			j++
		}
		values := []string{}
		for k := i; k < j; k++ {
			values = append(values, kva[k].Value)
		}
		output := reducef(kva[i].Key, values)
		fmt.Fprintf(ofile, "%v %v\n", kva[i].Key, output)
		i = j
	}
	ofile.Close()
}

func intermediateName(mapTask int, reduceTask int) string {
	return fmt.Sprintf("mr-%d-%d", mapTask, reduceTask)
}

func reportTask(taskType string, taskId int) {
	args := ReportTaskArgs{
		TaskType: taskType,
		TaskId:   taskId,
	}
	reply := ReportTaskReply{}
	call("Coordinator.ReportTask", &args, &reply)
}

type ByKey []KeyValue

func (a ByKey) Len() int           { return len(a) }
func (a ByKey) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByKey) Less(i, j int) bool { return a[i].Key < a[j].Key }

func CallExample() {
	args := ExampleArgs{}
	args.X = 99
	reply := ExampleReply{}
	ok := call("Coordinator.Example", &args, &reply)
	if ok {
		fmt.Printf("reply.Y %v\n", reply.Y)
	} else {
		fmt.Printf("call failed!\n")
	}
}

func call(rpcname string, args interface{}, reply interface{}) bool {
	c, err := rpc.DialHTTP("unix", coordSockName)
	if err != nil {
		log.Fatal("dialing:", err)
	}
	defer c.Close()

	if err := c.Call(rpcname, args, reply); err == nil {
		return true
	}
	log.Printf("%d: call failed err %v", os.Getpid(), err)
	return false
}
