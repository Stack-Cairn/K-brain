package imaging

import (
	"context"
	"io"
	"time"
)

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}
type Adapter interface {
	Submit(context.Context, ExecuteRequest) (RemoteResult, error)
	Poll(context.Context, string) (RemoteResult, error)
	Cancel(context.Context, string) error
	Reconcile(context.Context, string) (RemoteResult, error)
}
type RemoteResult struct {
	State         string         `json:"-"` // pending | completed | failed | unknown
	Handle        string         `json:"-"`
	Outputs       []OutputSource `json:"-"`
	Usage         *Usage         `json:"-"`
	NextPollAfter time.Duration  `json:"-"`
	ErrorCode     string         `json:"-"`
	ErrorReason   string         `json:"-"`
}
type OutputSource struct {
	Kind string `json:"-"` // bytes | url
	MIME string `json:"-"`
	Data []byte `json:"-"`
	URL  string `json:"-"`
	// UseProxy fetches URL through the app proxy, as the connection's own requests go.
	UseProxy bool `json:"-"`
}
type ExecuteRequest struct {
	ItemID        string           `json:"-"`
	Target        TargetSnapshot   `json:"-"`
	Request       CreateJobRequest `json:"-"`
	Inputs        []ResolvedAsset  `json:"-"`
	SubmissionKey string           `json:"-"`
	Continuation  []byte           `json:"-"`
}
type ResolvedAsset struct {
	AssetID string                        `json:"-"`
	MIME    string                        `json:"-"`
	Bytes   int64                         `json:"-"`
	Open    func() (io.ReadCloser, error) `json:"-"`
}

func (RemoteResult) MarshalJSON() ([]byte, error) {
	return nil, invalid("remote result cannot be serialized")
}
func (OutputSource) MarshalJSON() ([]byte, error) {
	return nil, invalid("output source cannot be serialized")
}
func (ExecuteRequest) MarshalJSON() ([]byte, error) {
	return nil, invalid("execution request cannot be serialized")
}
func (ResolvedAsset) MarshalJSON() ([]byte, error) {
	return nil, invalid("resolved asset cannot be serialized")
}
