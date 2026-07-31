package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
)

// DispatchTaskBatch creates tasks under one batch_id with idempotent replay.
func (r *Router) DispatchTaskBatch(
	ctx context.Context,
	batchID, callbackTopic, policyJSON, masterSessionID string,
	specs []TaskSpec,
	allowRedispatch bool,
) (*BatchDispatchResponse, error) {
	if batchID == "" {
		return nil, &Error{Msg: "batch_id is required"}
	}
	if len(specs) == 0 {
		return nil, &Error{Msg: "specs must not be empty"}
	}
	topic := callbackTopic
	if topic == "" {
		topic = "default"
	}
	normalized := make([]TaskSpec, len(specs))
	copy(normalized, specs)
	for i := range normalized {
		if normalized[i].CallbackTopic == "" {
			normalized[i].CallbackTopic = topic
		}
	}
	hash := batchSpecHash(batchID, normalized)

	existing, err := r.store.GetBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.BatchSpecHash != hash {
			return nil, &Error{Msg: "batch_id " + batchID + " already dispatched with different spec"}
		}
		tasks, err := r.store.ListTasks(ctx, ListTasksQuery{BatchID: batchID, Limit: 1000})
		if err != nil {
			return nil, err
		}
		responses := make([]DispatchResponse, 0, len(tasks))
		for _, task := range tasks {
			responses = append(responses, DispatchResponse{
				TaskID:        task.TaskID,
				CallbackTopic: task.CallbackTopic,
				Status:        task.Status,
				IdempotentHit: true,
				Attempt:       task.Attempt,
			})
		}
		return &BatchDispatchResponse{
			BatchID:       batchID,
			CallbackTopic: existing.CallbackTopic,
			Tasks:         responses,
			IdempotentHit: true,
		}, nil
	}

	if err := CheckDependencyCycles(normalized); err != nil {
		return nil, err
	}
	now := r.now()
	deadline := BatchDeadlineFromPolicy(policyJSON, now)
	if err := r.store.InsertBatch(ctx, &Batch{
		BatchID:         batchID,
		CallbackTopic:   topic,
		BatchSpecHash:   hash,
		PolicyJSON:      policyJSON,
		CreatedAt:       now,
		BatchDeadlineAt: deadline,
	}); err != nil {
		return nil, err
	}

	responses := make([]DispatchResponse, 0, len(normalized))
	for _, spec := range normalized {
		resp, err := r.dispatchSingle(ctx, spec, masterSessionID, allowRedispatch, batchID)
		if err != nil {
			return nil, err
		}
		responses = append(responses, *resp)
	}
	return &BatchDispatchResponse{
		BatchID:       batchID,
		CallbackTopic: topic,
		Tasks:         responses,
		IdempotentHit: false,
	}, nil
}

func batchSpecHash(batchID string, specs []TaskSpec) string {
	ordered := append([]TaskSpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].TaskID < ordered[j].TaskID
	})
	payload := struct {
		BatchID string     `json:"batch_id"`
		Specs   []TaskSpec `json:"specs"`
	}{BatchID: batchID, Specs: ordered}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return fmtHex(sum[:])
}

func fmtHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
