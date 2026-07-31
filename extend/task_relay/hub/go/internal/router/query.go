package router

// ListTasksQuery filters task rows for ListTasks RPC and claim scanning.
type ListTasksQuery struct {
	BatchID       string
	CallbackTopic string
	Statuses      []string
	Limit         int
}
