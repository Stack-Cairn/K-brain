package imaging

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// Failure reasons stay out of the WAL and snapshot. Released binaries decode those strictly
// and would refuse the whole store over one unknown field, so reasons live in a file they
// never open (docs/compatibility.md 6.2). The file is advisory: losing it only loses the hint.
const reasonsFileName = "error-reasons.v1.json"

type jobReasons struct {
	Job   string            `json:"job,omitempty"`
	Items map[string]string `json:"items,omitempty"`
	// Attempts holds each item's latest attempt reason, which later explains an unknown result.
	Attempts map[string]attemptReason `json:"attempts,omitempty"`
}

// attemptReason names its attempt: this file can lag the log, and one attempt's "not sent" must
// never describe a later attempt that may have been delivered and billed.
type attemptReason struct {
	No     int    `json:"no"`
	Reason string `json:"reason"`
}

// stripErrorReasons removes reasons from an encoded durable record. Only objects shaped like
// an ImageError are touched, so a user's request parameter named "reason" survives.
func stripErrorReasons(raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte(`"reason"`)) {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if !dropErrorReasons(value) {
		return raw, nil
	}
	return json.Marshal(value)
}

func dropErrorReasons(value any) (changed bool) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			changed = dropErrorReasons(item) || changed
		}
	case map[string]any:
		if isEncodedImageError(v) {
			delete(v, "reason")
			changed = true
		}
		for _, item := range v {
			changed = dropErrorReasons(item) || changed
		}
	}
	return changed
}

func isEncodedImageError(v map[string]any) bool {
	if _, ok := v["reason"].(string); !ok {
		return false
	}
	_, code := v["code"].(string)
	_, message := v["message"].(string)
	_, requestID := v["requestId"].(string)
	_, retryable := v["retryable"].(bool)
	return code && message && requestID && retryable
}

func reasonsOf(job ImageJob, execution executionDisk) jobReasons {
	r := jobReasons{}
	if job.Error != nil {
		r.Job = job.Error.Reason
	}
	for _, item := range job.Items {
		if item.Error != nil && item.Error.Reason != "" {
			if r.Items == nil {
				r.Items = map[string]string{}
			}
			r.Items[item.ID] = item.Error.Reason
		}
	}
	for id, item := range execution.Items {
		if a := lastAttempt(item); a != nil && a.Error != nil && a.Error.Reason != "" {
			if r.Attempts == nil {
				r.Attempts = map[string]attemptReason{}
			}
			r.Attempts[id] = attemptReason{No: a.AttemptNo, Reason: a.Error.Reason}
		}
	}
	return r
}

func lastAttempt(item itemExecutionDisk) *attemptDisk {
	if n := len(item.Attempts); n > 0 {
		return &item.Attempts[n-1]
	}
	return nil
}

// restoreReasons runs after recovery, before the store is shared. It also forgets jobs that
// are gone. Text is sanitized again because the file sits outside the checked log.
func (s *Store) restoreReasons() {
	s.reasons = map[string]jobReasons{}
	b, err := os.ReadFile(filepath.Join(s.dir, reasonsFileName))
	if err != nil {
		return
	}
	var saved map[string]jobReasons
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for id, r := range saved {
		record, ok := s.data.Jobs[id]
		if !ok {
			continue
		}
		s.reasons[id] = r
		if record.Value.Error != nil && record.Value.Error.Reason == "" {
			record.Value.Error.Reason = SafeReason(r.Job)
		}
		for i := range record.Value.Items {
			if e := record.Value.Items[i].Error; e != nil && e.Reason == "" {
				e.Reason = SafeReason(r.Items[record.Value.Items[i].ID])
			}
		}
		s.data.Jobs[id] = record
		for itemID, item := range s.data.Executions[id].Items {
			saved, ok := r.Attempts[itemID]
			if a := lastAttempt(item); ok && a != nil && a.AttemptNo == saved.No && a.Error != nil && a.Error.Reason == "" {
				a.Error.Reason = SafeReason(saved.Reason)
			}
		}
	}
}

// recordReasons saves the reasons of jobs a committed transaction changed. A failed write
// must not fail a transaction that is already durable, so the error is dropped.
func (s *Store) recordReasons(changed map[string]bool) {
	dirty := false
	for id := range changed {
		record, ok := s.data.Jobs[id]
		if !ok {
			if _, had := s.reasons[id]; had {
				delete(s.reasons, id)
				dirty = true
			}
			continue
		}
		r := reasonsOf(record.Value, s.data.Executions[id])
		if r.Job == "" && len(r.Items) == 0 && len(r.Attempts) == 0 {
			if _, had := s.reasons[id]; had {
				delete(s.reasons, id)
				dirty = true
			}
			continue
		}
		s.reasons[id] = r
		dirty = true
	}
	if !dirty {
		return
	}
	b, err := json.Marshal(s.reasons)
	if err != nil {
		return
	}
	temp, err := os.CreateTemp(s.dir, reasonsFileName+".*.tmp")
	if err != nil {
		return
	}
	name := temp.Name()
	_, err = temp.Write(b)
	if closeErr := temp.Close(); err != nil || closeErr != nil {
		os.Remove(name)
		return
	}
	if replaceStoreFile(name, filepath.Join(s.dir, reasonsFileName)) != nil {
		os.Remove(name)
	}
}
