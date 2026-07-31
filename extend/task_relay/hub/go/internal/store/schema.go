package store

// tasksSchema is the minimal SQLite tasks table compatible with the Python Hub.
const tasksSchema = `
CREATE TABLE IF NOT EXISTS tasks (
    task_id TEXT PRIMARY KEY,
    goal TEXT NOT NULL,
    callback_topic TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER DEFAULT 0,
    summary TEXT,
    created_at REAL NOT NULL,
    completed_at REAL
);
`
