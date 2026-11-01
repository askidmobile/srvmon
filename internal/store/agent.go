package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// AI agent state: chats with their display transcript, commands waiting for
// the owner's confirmation and scheduled tasks. The model-side context of a
// chat lives in the srvmon-ai container; srvmon keeps what the UI shows and
// everything that gates access to servers. Ids are AUTOINCREMENT: a run or
// tool call still in flight for a deleted row must never land on a new one.
const agentSchema = `CREATE TABLE agent_chats (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		server_id  INTEGER REFERENCES servers(id) ON DELETE SET NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE agent_messages (
		id      INTEGER PRIMARY KEY,
		chat_id INTEGER NOT NULL REFERENCES agent_chats(id) ON DELETE CASCADE,
		role    TEXT NOT NULL,             -- user | assistant
		text    TEXT NOT NULL DEFAULT '',
		tools   TEXT NOT NULL DEFAULT '[]', -- JSON []AgentToolCall
		error   TEXT NOT NULL DEFAULT '',
		ts      INTEGER NOT NULL
	);
	CREATE INDEX agent_messages_chat ON agent_messages(chat_id, id);
	CREATE TABLE agent_actions (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id     INTEGER,
		task_id     INTEGER,
		server_id   INTEGER NOT NULL,
		server_name TEXT NOT NULL,
		command     TEXT NOT NULL,
		reason      TEXT NOT NULL DEFAULT '',
		state       TEXT NOT NULL,         -- pending | approved | rejected | expired | done | failed
		created_at  INTEGER NOT NULL,
		decided_at  INTEGER,
		decided_by  TEXT NOT NULL DEFAULT '',
		exit_code   INTEGER,
		output      TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX agent_actions_state ON agent_actions(state, id);
	CREATE TABLE agent_tasks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		schedule    TEXT NOT NULL,
		prompt      TEXT NOT NULL,
		servers     TEXT NOT NULL DEFAULT '[]', -- JSON []string, empty = all
		notify      TEXT NOT NULL DEFAULT '',   -- '' | telegram
		enabled     INTEGER NOT NULL DEFAULT 1,
		created_at  INTEGER NOT NULL,
		next_run_at INTEGER,
		last_run_at INTEGER,
		last_status TEXT NOT NULL DEFAULT '',   -- ok | error | running
		last_result TEXT NOT NULL DEFAULT ''
	);`

// migrationAgentModel lets a chat use another model than the default one.
const migrationAgentModel = `ALTER TABLE agent_chats ADD COLUMN model TEXT NOT NULL DEFAULT '';`

// AgentChat is one conversation with the agent, optionally bound to a server.
// Model is empty for the default model from the AI settings.
type AgentChat struct {
	ID        int64     `json:"id"`
	ServerID  *int64    `json:"server_id,omitempty"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentToolCall is a tool call shown under an assistant message.
type AgentToolCall struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args,omitempty"`
	OK     bool            `json:"ok"`
	Output string          `json:"output,omitempty"`
}

// AgentMessage is one transcript entry.
type AgentMessage struct {
	ID    int64           `json:"id"`
	Role  string          `json:"role"`
	Text  string          `json:"text"`
	Tools []AgentToolCall `json:"tools"`
	Error string          `json:"error,omitempty"`
	TS    time.Time       `json:"ts"`
}

// CreateAgentChat starts a chat.
func (s *Store) CreateAgentChat(ctx context.Context, serverID *int64, title string) (AgentChat, error) {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_chats (server_id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		serverID, title, now.Unix(), now.Unix())
	if err != nil {
		return AgentChat{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetAgentChat(ctx, id)
}

func scanChat(row scanner) (AgentChat, error) {
	var c AgentChat
	var sid sql.NullInt64
	var created, updated int64
	if err := row.Scan(&c.ID, &sid, &c.Title, &c.Model, &created, &updated); err != nil {
		return c, err
	}
	if sid.Valid {
		c.ServerID = &sid.Int64
	}
	c.CreatedAt, c.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return c, nil
}

// GetAgentChat returns a chat or ErrNotFound.
func (s *Store) GetAgentChat(ctx context.Context, id int64) (AgentChat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx,
		`SELECT id, server_id, title, model, created_at, updated_at FROM agent_chats WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SetAgentChatModel sets the chat's model ("" = the default one).
func (s *Store) SetAgentChatModel(ctx context.Context, id int64, model string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_chats SET model = ? WHERE id = ?`, model, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAgentChats returns the latest chats first.
func (s *Store) ListAgentChats(ctx context.Context, limit int) ([]AgentChat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, title, model, created_at, updated_at FROM agent_chats ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentChat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteAgentChat removes a chat and its transcript.
// AgentChatByTitlePrefix returns the newest chat whose title starts with
// prefix, or ErrNotFound.
func (s *Store) AgentChatByTitlePrefix(ctx context.Context, prefix string) (AgentChat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx,
		`SELECT id, server_id, title, model, created_at, updated_at FROM agent_chats
		 WHERE substr(title, 1, length(?1)) = ?1 ORDER BY id DESC LIMIT 1`, prefix))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) DeleteAgentChat(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_chats WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAgentMessage appends to a chat transcript and bumps the chat; the first
// user message names the chat.
func (s *Store) AddAgentMessage(ctx context.Context, chatID int64, m AgentMessage) (int64, error) {
	if m.Tools == nil {
		m.Tools = []AgentToolCall{}
	}
	tools, _ := json.Marshal(m.Tools)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_messages (chat_id, role, text, tools, error, ts) VALUES (?, ?, ?, ?, ?, ?)`,
		chatID, m.Role, m.Text, string(tools), m.Error, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_chats SET updated_at = ? WHERE id = ?`, now, chatID); err != nil {
		return 0, err
	}
	if m.Role == "user" {
		title := []rune(m.Text)
		if len(title) > 60 {
			title = append(title[:59], '…')
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_chats SET title = ? WHERE id = ? AND title = ''`, string(title), chatID); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// AgentMessages returns a chat transcript in order.
func (s *Store) AgentMessages(ctx context.Context, chatID int64) ([]AgentMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, role, text, tools, error, ts FROM agent_messages WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentMessage{}
	for rows.Next() {
		var m AgentMessage
		var tools string
		var ts int64
		if err := rows.Scan(&m.ID, &m.Role, &m.Text, &tools, &m.Error, &ts); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tools), &m.Tools)
		if m.Tools == nil {
			m.Tools = []AgentToolCall{}
		}
		m.TS = time.Unix(ts, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- confirmations ---

// Agent action states.
const (
	ActionPending  = "pending"
	ActionApproved = "approved"
	ActionRejected = "rejected"
	ActionExpired  = "expired"
	ActionDone     = "done"
	ActionFailed   = "failed"
)

// AgentAction is a command the agent asked to run that changes a server.
type AgentAction struct {
	ID         int64      `json:"id"`
	ChatID     *int64     `json:"chat_id,omitempty"`
	TaskID     *int64     `json:"task_id,omitempty"`
	ServerID   int64      `json:"server_id"`
	ServerName string     `json:"server"`
	Command    string     `json:"command"`
	Reason     string     `json:"reason"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	DecidedBy  string     `json:"decided_by,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Output     string     `json:"output,omitempty"`
	// Source is empty for the built-in agent and "mcp:<token name>" for a
	// command from an MCP client.
	Source string `json:"source,omitempty"`
}

const actionCols = `id, chat_id, task_id, server_id, server_name, command, reason, state, created_at, decided_at, decided_by, exit_code, output, source`

func scanAction(row scanner) (AgentAction, error) {
	var a AgentAction
	var chat, task, decided, code sql.NullInt64
	var created int64
	if err := row.Scan(&a.ID, &chat, &task, &a.ServerID, &a.ServerName, &a.Command, &a.Reason, &a.State,
		&created, &decided, &a.DecidedBy, &code, &a.Output, &a.Source); err != nil {
		return a, err
	}
	if chat.Valid {
		a.ChatID = &chat.Int64
	}
	if task.Valid {
		a.TaskID = &task.Int64
	}
	if decided.Valid {
		t := time.Unix(decided.Int64, 0).UTC()
		a.DecidedAt = &t
	}
	if code.Valid {
		c := int(code.Int64)
		a.ExitCode = &c
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, nil
}

// CreateAgentAction records a pending command.
func (s *Store) CreateAgentAction(ctx context.Context, a AgentAction) (AgentAction, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_actions
		(chat_id, task_id, server_id, server_name, command, reason, state, created_at, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ChatID, a.TaskID, a.ServerID, a.ServerName, a.Command, a.Reason, ActionPending, time.Now().Unix(), a.Source)
	if err != nil {
		return a, err
	}
	id, _ := res.LastInsertId()
	return s.GetAgentAction(ctx, id)
}

// GetAgentAction returns an action or ErrNotFound.
func (s *Store) GetAgentAction(ctx context.Context, id int64) (AgentAction, error) {
	a, err := scanAction(s.db.QueryRowContext(ctx, `SELECT `+actionCols+` FROM agent_actions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// ListAgentActions returns actions, newest first. state "" lists all.
func (s *Store) ListAgentActions(ctx context.Context, state string, limit int) ([]AgentAction, error) {
	q := `SELECT ` + actionCols + ` FROM agent_actions`
	args := []any{}
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentAction{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideAgentAction moves a pending action to approved, rejected or expired.
// It returns false when the action is no longer pending (decided elsewhere).
func (s *Store) DecideAgentAction(ctx context.Context, id int64, state, by string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_actions SET state = ?, decided_at = ?, decided_by = ?
		WHERE id = ? AND state = ?`, state, time.Now().Unix(), by, id, ActionPending)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishAgentAction stores the result of an approved command.
func (s *Store) FinishAgentAction(ctx context.Context, id int64, ok bool, exitCode int, output string) error {
	state := ActionDone
	if !ok {
		state = ActionFailed
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_actions SET state = ?, exit_code = ?, output = ? WHERE id = ?`,
		state, exitCode, output, id)
	return err
}

// ExpirePendingAgentActions expires every pending action: nobody waits for
// them after a restart.
func (s *Store) ExpirePendingAgentActions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_actions SET state = ?, decided_at = ?, decided_by = 'restart'
		WHERE state = ?`, ActionExpired, time.Now().Unix(), ActionPending)
	return err
}

// --- tasks ---

// AgentTask is a scheduled prompt for the agent.
type AgentTask struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Schedule   string     `json:"schedule"`
	Prompt     string     `json:"prompt"`
	Servers    []string   `json:"servers"`
	Notify     string     `json:"notify"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastStatus string     `json:"last_status"`
	LastResult string     `json:"last_result"`
}

const taskCols = `id, name, schedule, prompt, servers, notify, enabled, created_at, next_run_at, last_run_at, last_status, last_result`

func unixPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

func timeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func scanTask(row scanner) (AgentTask, error) {
	var t AgentTask
	var servers string
	var created int64
	var next, last sql.NullInt64
	if err := row.Scan(&t.ID, &t.Name, &t.Schedule, &t.Prompt, &servers, &t.Notify, &t.Enabled, &created,
		&next, &last, &t.LastStatus, &t.LastResult); err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(servers), &t.Servers)
	if t.Servers == nil {
		t.Servers = []string{}
	}
	t.CreatedAt = time.Unix(created, 0).UTC()
	t.NextRunAt, t.LastRunAt = unixPtr(next), unixPtr(last)
	return t, nil
}

// CreateAgentTask stores a task.
func (s *Store) CreateAgentTask(ctx context.Context, t AgentTask) (AgentTask, error) {
	if t.Servers == nil {
		t.Servers = []string{}
	}
	servers, _ := json.Marshal(t.Servers)
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_tasks (name, schedule, prompt, servers, notify, enabled, created_at, next_run_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.Name, t.Schedule, t.Prompt, string(servers), t.Notify, t.Enabled,
		time.Now().Unix(), timeArg(t.NextRunAt))
	if err != nil {
		return t, err
	}
	id, _ := res.LastInsertId()
	return s.GetAgentTask(ctx, id)
}

// UpdateAgentTask replaces the editable fields of a task.
func (s *Store) UpdateAgentTask(ctx context.Context, t AgentTask) error {
	if t.Servers == nil {
		t.Servers = []string{}
	}
	servers, _ := json.Marshal(t.Servers)
	res, err := s.db.ExecContext(ctx, `UPDATE agent_tasks SET name = ?, schedule = ?, prompt = ?, servers = ?, notify = ?,
		enabled = ?, next_run_at = ? WHERE id = ?`, t.Name, t.Schedule, t.Prompt, string(servers), t.Notify, t.Enabled,
		timeArg(t.NextRunAt), t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAgentTask returns a task or ErrNotFound.
func (s *Store) GetAgentTask(ctx context.Context, id int64) (AgentTask, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM agent_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// ListAgentTasks returns all tasks.
func (s *Store) ListAgentTasks(ctx context.Context) ([]AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskCols+` FROM agent_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAgentTask removes a task.
func (s *Store) DeleteAgentTask(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimAgentTask marks a task as running and moves its next run; it returns
// false when the task is already running (a manual run raced the scheduler).
func (s *Store) ClaimAgentTask(ctx context.Context, id int64, next *time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_tasks SET last_status = 'running', last_run_at = ?, next_run_at = ?
		WHERE id = ? AND last_status != 'running'`, time.Now().Unix(), timeArg(next), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishAgentTask stores the outcome of a run.
func (s *Store) FinishAgentTask(ctx context.Context, id int64, status, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_tasks SET last_status = ?, last_result = ? WHERE id = ?`, status, result, id)
	return err
}

// ResetRunningAgentTasks clears 'running' left by a restart.
func (s *Store) ResetRunningAgentTasks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_tasks SET last_status = 'error', last_result = ?
		WHERE last_status = 'running'`, i18n.T("interrupted by a srvmon restart"))
	return err
}
