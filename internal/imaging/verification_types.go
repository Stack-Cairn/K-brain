package imaging

import "time"

// VerificationSummary is read-only public evidence. It contains no binding
// fingerprint, credential reference, provider handle, endpoint or output URL.
type VerificationSummary struct {
	State          string     `json:"state"`
	JobID          string     `json:"jobId,omitempty"`
	LiveVerifiedAt *time.Time `json:"liveVerifiedAt,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

type VerifyTargetRequest struct {
	ExpectedRevision *uint64   `json:"expectedRevision"`
	ProjectID        string    `json:"projectId"`
	ConnectionID     string    `json:"connectionId"`
	RemoteID         string    `json:"remoteId"`
	TargetRevision   *uint64   `json:"targetRevision"`
	Operation        Operation `json:"operation"`
	Count            int       `json:"count"`
	Prompt           string    `json:"prompt"`
	ConfirmPaid      bool      `json:"confirmPaid"`
}

type VerificationReceipt struct {
	ID               string    `json:"id"`
	TargetID         string    `json:"targetId"`
	JobID            string    `json:"jobId,omitempty"`
	State            string    `json:"state"`
	JobStatus        JobStatus `json:"jobStatus,omitempty"`
	SettingsRevision uint64    `json:"settingsRevision"`
	Reason           string    `json:"reason,omitempty"`
}
