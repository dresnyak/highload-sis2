package groupclient

const RetryServiceConfig = `{
  "methodConfig": [{
    "name": [
      {"service": "tasktracker.v1.GroupService", "method": "CheckMembership"},
      {"service": "tasktracker.v1.TaskService", "method": "GetTask"},
      {"service": "tasktracker.v1.TaskService", "method": "ListTasks"},
      {"service": "tasktracker.v1.TaskService", "method": "CreateTask"},
      {"service": "tasktracker.v1.TaskService", "method": "DeleteTask"}
    ],
    "retryPolicy": {
      "maxAttempts": 3,
      "initialBackoff": "0.02s",
      "maxBackoff": "0.2s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`
