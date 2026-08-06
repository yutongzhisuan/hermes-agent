package registry

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/infa/task_relay/hub/internal/metrics"
	"github.com/infa/task_relay/hub/internal/router"
)

// Pusher delivers task.run to an online Mode C session.
type Pusher interface {
	PushTaskRun(payload map[string]any) bool
}

// WorkerClaims mirrors JWT scope used during poll eligibility.
type WorkerClaims struct {
	AllowedToolsets []string
	MaxConcurrent   int
}

// Worker is the Hub-visible worker row.
type Worker struct {
	WorkerID        string
	Status          string
	SessionModes    []string
	MaxConcurrent   int
	RunningTasks    int
	CreditAvailable int
	Toolsets        []string
	ResourcesJSON   string
	LoadJSON        string
	OS              string
	Arch            string
	Region          string
	LastAnnounce    time.Time
	LastHeartbeat   time.Time
	WakeURL         string
	OnlineSessionID string
	DrainRequested  bool
}

// AnnounceInput carries worker.announce payload fields.
type AnnounceInput struct {
	WorkerID        string
	SessionModes    []string
	MaxConcurrent   int
	InitialCredit   *int
	Toolsets        []string
	Capabilities    map[string]any
	WakeURL         string
	OnlineSessionID string
	Pusher          Pusher
}

// WorkerStore persists worker registry rows.
type WorkerStore interface {
	UpsertWorker(ctx context.Context, worker *router.Worker) error
	ListWorkers(ctx context.Context, onlySchedulable bool) ([]*router.Worker, error)
}

// Registry tracks live workers for ListWorkers, poll eligibility, and Mode C push.
type Registry struct {
	mu      sync.RWMutex
	store   WorkerStore
	workers map[string]*Worker
	pushers map[string]Pusher
	now     func() time.Time
}

// New returns an empty worker registry.
func New(store WorkerStore) *Registry {
	return &Registry{
		store:   store,
		workers: make(map[string]*Worker),
		pushers: make(map[string]Pusher),
		now:     time.Now,
	}
}

// LoadFromStore hydrates in-memory workers from persistence.
func (r *Registry) LoadFromStore(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	rows, err := r.store.ListWorkers(ctx, false)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range rows {
		if row == nil || row.WorkerID == "" {
			continue
		}
		r.workers[row.WorkerID] = workerFromStore(row)
	}
	return nil
}

// Announce upserts a worker row after worker.announce.
func (r *Registry) Announce(ctx context.Context, input AnnounceInput) {
	if input.WorkerID == "" {
		return
	}
	modes := input.SessionModes
	if len(modes) == 0 {
		modes = []string{"A"}
	}
	toolsets := append([]string(nil), input.Toolsets...)
	osName := stringField(input.Capabilities, "os")
	arch := stringField(input.Capabilities, "arch")
	region := stringField(input.Capabilities, "region")
	resourcesJSON := resourcesJSONFromCapabilities(input.Capabilities)
	if len(toolsets) == 0 {
		if capsToolsets, ok := input.Capabilities["toolsets"].([]any); ok {
			for _, item := range capsToolsets {
				if s, ok := item.(string); ok && s != "" {
					toolsets = append(toolsets, s)
				}
			}
		}
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	existing := r.workers[input.WorkerID]
	running := 0
	credit := 0
	if existing != nil {
		running = existing.RunningTasks
		credit = existing.CreditAvailable
	}
	maxConcurrent := input.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if input.InitialCredit != nil && supportsMode(modes, "C") {
		credit = max(0, min(*input.InitialCredit, maxConcurrent))
	}
	status := "idle"
	if existing != nil && existing.DrainRequested {
		status = "draining"
	}
	r.workers[input.WorkerID] = &Worker{
		WorkerID:        input.WorkerID,
		Status:          status,
		SessionModes:    modes,
		MaxConcurrent:   maxConcurrent,
		RunningTasks:    running,
		CreditAvailable: credit,
		Toolsets:        toolsets,
		ResourcesJSON:   resourcesJSON,
		OS:              osName,
		Arch:            arch,
		Region:          region,
		LastAnnounce:    now,
		LastHeartbeat:   now,
		WakeURL:         input.WakeURL,
		OnlineSessionID: input.OnlineSessionID,
		DrainRequested:  existing != nil && existing.DrainRequested,
	}
	if input.Pusher != nil {
		r.pushers[input.WorkerID] = input.Pusher
	}
	r.persistLocked(ctx, r.workers[input.WorkerID])
	r.refreshWorkerSessionMetricsLocked()
}

// CloseSession marks a worker offline when the active session closes gracefully.
func (r *Registry) CloseSession(ctx context.Context, workerID, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	worker, ok := r.workers[workerID]
	if !ok || worker.OnlineSessionID != sessionID {
		return
	}
	worker.OnlineSessionID = ""
	worker.Status = "offline"
	delete(r.pushers, workerID)
	r.persistLocked(ctx, worker)
	r.refreshWorkerSessionMetricsLocked()
}

// Get returns a worker copy or nil.
func (r *Registry) Get(workerID string) *Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	worker, ok := r.workers[workerID]
	if !ok {
		return nil
	}
	copy := *worker
	return &copy
}

// PushTaskRun sends task.run to the worker session when online.
func (r *Registry) PushTaskRun(workerID string, payload map[string]any) bool {
	r.mu.RLock()
	pusher := r.pushers[workerID]
	r.mu.RUnlock()
	if pusher == nil {
		return false
	}
	return pusher.PushTaskRun(payload)
}

// SetCredit updates Mode C push credit for a worker.
func (r *Registry) SetCredit(workerID string, credit int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	worker, ok := r.workers[workerID]
	if !ok {
		return 0
	}
	free := worker.MaxConcurrent - worker.RunningTasks
	if free < 0 {
		free = 0
	}
	worker.CreditAvailable = max(0, min(credit, free))
	return worker.CreditAvailable
}

// ConsumeCredit decrements push credit after a successful Mode C push.
func (r *Registry) ConsumeCredit(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if worker, ok := r.workers[workerID]; ok && worker.CreditAvailable > 0 {
		worker.CreditAvailable--
	}
}

// ReleaseCredit returns one credit slot after terminal completion.
func (r *Registry) ReleaseCredit(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	worker, ok := r.workers[workerID]
	if !ok {
		return
	}
	worker.CreditAvailable = min(worker.MaxConcurrent, worker.CreditAvailable+1)
}

// IncRunning increments running task count for a worker.
func (r *Registry) IncRunning(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if worker, ok := r.workers[workerID]; ok {
		worker.RunningTasks++
		if worker.RunningTasks >= worker.MaxConcurrent {
			worker.Status = "busy"
		}
	}
}

// DecRunning decrements running task count.
func (r *Registry) DecRunning(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	worker, ok := r.workers[workerID]
	if !ok {
		return
	}
	if worker.RunningTasks > 0 {
		worker.RunningTasks--
	}
	if worker.DrainRequested {
		worker.Status = "draining"
	} else if worker.RunningTasks < worker.MaxConcurrent {
		worker.Status = "idle"
	}
}

// HeartbeatInput carries optional load/resources updates from worker.heartbeat.
type HeartbeatInput struct {
	Load      map[string]any
	Resources map[string]any
}

// Heartbeat refreshes liveness for an online worker session.
func (r *Registry) Heartbeat(ctx context.Context, workerID, sessionID string, input *HeartbeatInput) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	worker, ok := r.workers[workerID]
	if !ok || worker.OnlineSessionID != sessionID {
		return false
	}
	now := r.now()
	worker.LastHeartbeat = now
	if input != nil {
		if len(input.Load) > 0 {
			if raw, err := json.Marshal(input.Load); err == nil {
				worker.LoadJSON = string(raw)
			}
		}
		if len(input.Resources) > 0 {
			if raw, err := json.Marshal(input.Resources); err == nil {
				worker.ResourcesJSON = string(raw)
			}
		}
	}
	if worker.Status == "stale" {
		if worker.DrainRequested {
			worker.Status = "draining"
		} else {
			worker.Status = "idle"
		}
	}
	okResult := true
	r.persistLocked(ctx, worker)
	return okResult
}

// MarkStale marks workers whose heartbeat is older than deadline.
func (r *Registry) MarkStale(ctx context.Context, deadline time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, worker := range r.workers {
		if worker.Status == "offline" {
			continue
		}
		if worker.LastHeartbeat.Before(deadline) {
			worker.Status = "stale"
			r.persistLocked(ctx, worker)
		}
	}
}

// Drain marks a worker as draining and stops new offers.
func (r *Registry) Drain(ctx context.Context, workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if worker, ok := r.workers[workerID]; ok {
		worker.DrainRequested = true
		worker.Status = "draining"
		r.persistLocked(ctx, worker)
	}
}

// UnregisterSession removes push handle when WS disconnects.
func (r *Registry) UnregisterSession(ctx context.Context, workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pushers, workerID)
	if worker, ok := r.workers[workerID]; ok {
		worker.OnlineSessionID = ""
		worker.Status = "offline"
		r.persistLocked(ctx, worker)
	}
	r.refreshWorkerSessionMetricsLocked()
}

// SupportsMode reports whether worker advertises a session mode.
func SupportsMode(worker *Worker, mode string) bool {
	if worker == nil {
		return false
	}
	return supportsMode(worker.SessionModes, mode)
}

func supportsMode(modes []string, mode string) bool {
	target := mode
	for _, item := range modes {
		if len(item) == 1 && (item[0]|32) == target[0]|32 {
			return true
		}
	}
	return false
}

// IsEligibleForPoll checks ACL/capability gates before claim (M1 subset).
func IsEligibleForPoll(worker *Worker, task *routerTaskView, claims *WorkerClaims) bool {
	if worker == nil || task == nil {
		return false
	}
	if !SupportsMode(worker, "A") {
		return false
	}
	if worker.Status == "offline" || worker.Status == "stale" || worker.Status == "draining" {
		return false
	}
	if task.TargetWorker != "" && worker.WorkerID != task.TargetWorker {
		return false
	}
	if denied := decodeStringList(task.DenyWorkerIDsJSON); len(denied) > 0 {
		if slices.Contains(denied, worker.WorkerID) {
			return false
		}
	}
	if allowed := decodeStringList(task.AllowedWorkerIDsJSON); len(allowed) > 0 {
		found := slices.Contains(allowed, worker.WorkerID)
		if !found {
			return false
		}
	}
	if len(task.Toolsets) > 0 {
		authorized := worker.Toolsets
		if claims != nil && len(claims.AllowedToolsets) > 0 {
			authorized = intersectToolsets(worker.Toolsets, claims.AllowedToolsets)
		}
		if !isSubset(task.Toolsets, authorized) {
			return false
		}
	}
	return true
}

// routerTaskView is a minimal task projection for eligibility checks.
type routerTaskView struct {
	TargetWorker         string
	Toolsets             []string
	AllowedWorkerIDsJSON string
	DenyWorkerIDsJSON    string
}

// TaskView builds eligibility input from router task fields.
func TaskView(targetWorker, toolsetsJSON, allowedJSON, denyJSON string) *routerTaskView {
	var toolsets []string
	if toolsetsJSON != "" {
		_ = json.Unmarshal([]byte(toolsetsJSON), &toolsets)
	}
	return &routerTaskView{
		TargetWorker:         targetWorker,
		Toolsets:             toolsets,
		AllowedWorkerIDsJSON: allowedJSON,
		DenyWorkerIDsJSON:    denyJSON,
	}
}

func decodeStringList(raw string) []string {
	if raw == "" {
		return nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

func intersectToolsets(left, right []string) []string {
	set := make(map[string]struct{}, len(right))
	for _, item := range right {
		set[item] = struct{}{}
	}
	out := make([]string, 0)
	for _, item := range left {
		if _, ok := set[item]; ok {
			out = append(out, item)
		}
	}
	return out
}

func isSubset(required, available []string) bool {
	set := make(map[string]struct{}, len(available))
	for _, item := range available {
		set[item] = struct{}{}
	}
	for _, item := range required {
		if _, ok := set[item]; !ok {
			return false
		}
	}
	return true
}

// List returns workers optionally filtered to schedulable rows.
func (r *Registry) List(onlySchedulable bool) []Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Worker, 0, len(r.workers))
	for _, worker := range r.workers {
		if onlySchedulable && worker.Status != "idle" && worker.Status != "busy" {
			continue
		}
		out = append(out, *worker)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].WorkerID < out[j].WorkerID
	})
	return out
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw, ok := values[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return s
}

func resourcesJSONFromCapabilities(caps map[string]any) string {
	if caps == nil {
		return ""
	}
	if res, ok := caps["resources"].(map[string]any); ok {
		raw, _ := json.Marshal(res)
		return string(raw)
	}
	payload := map[string]any{}
	for _, key := range []string{"cpu_cores", "memory_gb", "gpu_count", "network_profiles", "network_profile"} {
		if value, ok := caps[key]; ok {
			payload[key] = value
		}
	}
	if len(payload) == 0 {
		return ""
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func (r *Registry) persistLocked(ctx context.Context, worker *Worker) {
	if r == nil || r.store == nil || worker == nil {
		return
	}
	_ = r.store.UpsertWorker(ctx, workerToStore(worker, r.now()))
}

func workerToStore(worker *Worker, now time.Time) *router.Worker {
	caps := map[string]any{"toolsets": append([]string(nil), worker.Toolsets...)}
	if worker.OS != "" {
		caps["os"] = worker.OS
	}
	if worker.Arch != "" {
		caps["arch"] = worker.Arch
	}
	if worker.Region != "" {
		caps["region"] = worker.Region
	}
	capsJSON, _ := json.Marshal(caps)
	lastSeen := worker.LastHeartbeat
	if lastSeen.IsZero() {
		lastSeen = now
	}
	return &router.Worker{
		WorkerID:         worker.WorkerID,
		WakeURL:          worker.WakeURL,
		SessionModes:     encodeSessionModes(worker.SessionModes),
		CapabilitiesJSON: string(capsJSON),
		ResourcesJSON:    worker.ResourcesJSON,
		LoadJSON:         worker.LoadJSON,
		MaxConcurrent:    worker.MaxConcurrent,
		CreditAvailable:  worker.CreditAvailable,
		RunningTasks:     worker.RunningTasks,
		LastAnnounceAt:   worker.LastAnnounce,
		LastHeartbeatAt:  worker.LastHeartbeat,
		LastSeenAt:       lastSeen,
		Status:           worker.Status,
		OnlineSessionID:  worker.OnlineSessionID,
		DrainRequested:   worker.DrainRequested,
	}
}

func workerFromStore(row *router.Worker) *Worker {
	toolsets := []string{}
	osName, arch, region := "", "", ""
	if row.CapabilitiesJSON != "" {
		var caps map[string]any
		if err := json.Unmarshal([]byte(row.CapabilitiesJSON), &caps); err == nil {
			osName = stringField(caps, "os")
			arch = stringField(caps, "arch")
			region = stringField(caps, "region")
			if raw, ok := caps["toolsets"].([]any); ok {
				for _, item := range raw {
					if s, ok := item.(string); ok && s != "" {
						toolsets = append(toolsets, s)
					}
				}
			}
		}
	}
	lastHeartbeat := row.LastHeartbeatAt
	if lastHeartbeat.IsZero() {
		lastHeartbeat = row.LastSeenAt
	}
	return &Worker{
		WorkerID:        row.WorkerID,
		Status:          row.Status,
		SessionModes:    decodeSessionModes(row.SessionModes),
		MaxConcurrent:   row.MaxConcurrent,
		RunningTasks:    row.RunningTasks,
		CreditAvailable: row.CreditAvailable,
		Toolsets:        toolsets,
		ResourcesJSON:   row.ResourcesJSON,
		LoadJSON:        row.LoadJSON,
		OS:              osName,
		Arch:            arch,
		Region:          region,
		LastAnnounce:    row.LastAnnounceAt,
		LastHeartbeat:   lastHeartbeat,
		WakeURL:         row.WakeURL,
		OnlineSessionID: row.OnlineSessionID,
		DrainRequested:  row.DrainRequested,
	}
}

func encodeSessionModes(modes []string) string {
	if len(modes) == 0 {
		return "A"
	}
	var b strings.Builder
	for _, mode := range modes {
		mode = strings.TrimSpace(mode)
		if mode == "" {
			continue
		}
		if len(mode) == 1 {
			b.WriteByte(strings.ToUpper(mode)[0])
			continue
		}
		b.WriteString(strings.ToUpper(mode))
	}
	if b.Len() == 0 {
		return "A"
	}
	return b.String()
}

func decodeSessionModes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"A"}
	}
	modes := make([]string, 0, len(raw))
	for _, ch := range strings.ToUpper(raw) {
		modes = append(modes, string(ch))
	}
	return modes
}

func (r *Registry) refreshWorkerSessionMetricsLocked() {
	for _, worker := range r.workers {
		if worker == nil {
			continue
		}
		modes := strings.ToLower(strings.Join(worker.SessionModes, ","))
		value := 0.0
		if worker.OnlineSessionID != "" {
			value = 1.0
		}
		metrics.SetGauge("relay_worker_sessions_active", map[string]string{
			"worker_id":     worker.WorkerID,
			"session_modes": modes,
		}, value)
	}
}
