package imaging

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	CodeInvalidInput         = "IMAGE_INVALID_INPUT"
	CodeUnsupportedOperation = "IMAGE_UNSUPPORTED_OPERATION"
	CodeRevisionConflict     = "IMAGE_REVISION_CONFLICT"
	CodeIdempotencyConflict  = "IMAGE_IDEMPOTENCY_CONFLICT"
	CodeConnectionInUse      = "IMAGE_CONNECTION_IN_USE"
	CodeAuthRequired         = "IMAGE_AUTH_REQUIRED"
	CodeRateLimited          = "IMAGE_RATE_LIMITED"
	CodeEventGap             = "IMAGE_EVENT_GAP"
	CodeNotFound             = "IMAGE_NOT_FOUND"
	CodeForbidden            = "IMAGE_FORBIDDEN"
	CodeUnavailable          = "IMAGE_UNAVAILABLE"
	CodeNoImageOutput        = "IMAGE_NO_IMAGE_OUTPUT"
	CodeResultUnknown        = "IMAGE_RESULT_UNKNOWN"
	CodeTooLarge             = "IMAGE_TOO_LARGE"
)

var ErrUnsupported = errors.New(CodeUnsupportedOperation)

type ImageError struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	RequestID string        `json:"requestId"`
	Retryable bool          `json:"retryable"`
	Details   *ErrorDetails `json:"details,omitempty"`
}

// ErrorDetails is a whitelist. Raw upstream bodies, headers and causes are excluded.
type ErrorDetails struct {
	ParameterKey      string  `json:"parameterKey,omitempty"`
	ConnectionID      string  `json:"connectionId,omitempty"`
	TargetID          string  `json:"targetId,omitempty"`
	ExpectedRevision  *uint64 `json:"expectedRevision,omitempty"`
	ActualRevision    *uint64 `json:"actualRevision,omitempty"`
	FirstAvailableSeq *uint64 `json:"firstAvailableSeq,omitempty"`
	LastEventSeq      *uint64 `json:"lastEventSeq,omitempty"`
	// Reason is a backend-authored validation note, never upstream text.
	Reason string `json:"reason,omitempty"`
}

func (e ImageError) Error() string { return e.Code + ": " + errorMessage(e.Code) }
func (e ImageError) MarshalJSON() ([]byte, error) {
	type wire ImageError
	return json.Marshal(wire(*PublicError(&e)))
}
func (d ErrorDetails) MarshalJSON() ([]byte, error) {
	type wire ErrorDetails
	var ns []uint64
	for _, p := range []*uint64{d.ExpectedRevision, d.ActualRevision, d.FirstAvailableSeq, d.LastEventSeq} {
		if p != nil {
			ns = append(ns, *p)
		}
	}
	return safeMarshal(wire(d), ns...)
}
func PublicError(e *ImageError) *ImageError {
	if e == nil {
		return nil
	}
	code := e.Code
	if errorMessage(code) == "Image operation failed" {
		code = CodeUnavailable
	}
	v := &ImageError{Code: code, Message: errorMessage(code), RequestID: e.RequestID, Retryable: e.Retryable}
	if e.Details != nil {
		d := e.Details
		v.Details = &ErrorDetails{ParameterKey: d.ParameterKey, ConnectionID: d.ConnectionID, TargetID: d.TargetID, ExpectedRevision: cloneUint(d.ExpectedRevision), ActualRevision: cloneUint(d.ActualRevision), FirstAvailableSeq: cloneUint(d.FirstAvailableSeq), LastEventSeq: cloneUint(d.LastEventSeq), Reason: d.Reason}
	}
	return v
}
func cloneUint(v *uint64) *uint64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
func errorMessage(code string) string {
	switch code {
	case CodeInvalidInput:
		return "Invalid image input"
	case CodeUnsupportedOperation:
		return "Image operation is unsupported"
	case CodeRevisionConflict:
		return "Image configuration revision changed"
	case CodeIdempotencyConflict:
		return "Idempotency key belongs to a different request"
	case CodeConnectionInUse:
		return "Image connection is in use"
	case CodeAuthRequired:
		return "Image authentication is required"
	case CodeRateLimited:
		return "Image request limit reached"
	case CodeEventGap:
		return "Image event history is no longer available"
	case CodeNotFound:
		return "Image object not found"
	case CodeForbidden:
		return "Image access denied"
	case CodeUnavailable:
		return "Image module unavailable"
	case CodeNoImageOutput:
		return "Provider returned no image output"
	case CodeTooLarge:
		return "Image exceeds the upload size limit"
	case CodeResultUnknown:
		return "Image execution result is unknown"
	default:
		return "Image operation failed"
	}
}
func invalid(reason string) error { return fmt.Errorf("%s: %s", CodeInvalidInput, reason) }

type Delivery string

const (
	DeliveryNotSent   Delivery = "not_sent"
	DeliveryRejected  Delivery = "rejected"
	DeliveryUncertain Delivery = "uncertain"
)

type AdapterError struct {
	Code       string        `json:"code"`
	Delivery   Delivery      `json:"delivery"`
	RetryAfter time.Duration `json:"-"`
	Cause      error         `json:"-"`
}

func (e *AdapterError) Error() string {
	if e == nil {
		return CodeUnavailable
	}
	return e.Code + ": " + errorMessage(e.Code)
}
func (e *AdapterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func (e AdapterError) MarshalJSON() ([]byte, error) {
	if !e.Delivery.Valid() {
		return nil, invalid("invalid delivery state")
	}
	return json.Marshal(struct {
		Code     string   `json:"code"`
		Delivery Delivery `json:"delivery"`
	}{PublicError(&ImageError{Code: e.Code}).Code, e.Delivery})
}
func (d Delivery) Valid() bool {
	return d == DeliveryNotSent || d == DeliveryRejected || d == DeliveryUncertain
}
