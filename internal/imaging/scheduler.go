package imaging

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

type engineWork struct {
	scope     Scope
	job       ImageJob
	execution ExecutionRecord
	index     int
	kind      string
	pollDelay time.Duration
}

var imageSchedulers = struct {
	sync.Mutex
	owners map[*Store]*Scheduler
}{owners: make(map[*Store]*Scheduler)}

func (w engineWork) key() string { return w.job.ID + "/" + w.job.Items[w.index].ID }
func (w engineWork) group() string {
	return w.scope.OwnerID + "\x00" + w.scope.BackendID + "\x00" + w.job.TargetSnapshot.ConnectionID
}

type Scheduler struct {
	service         *Service
	mu              sync.Mutex
	started, closed bool
	cancel          context.CancelFunc
	done            chan struct{}
	owner           string
	running         map[string]engineWork // exclusively owned by the dispatcher
	finished        chan string
	lastGroup       string
	err             error
}

func NewScheduler(s *Service) *Scheduler {
	return &Scheduler{service: s, done: make(chan struct{}), owner: engineID("lease_"), running: map[string]engineWork{}, finished: make(chan string, 16)}
}
func (q *Scheduler) Start(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return &ImageError{Code: CodeUnavailable}
	}
	if q.started {
		return nil
	}
	s := q.service
	imageSchedulers.Lock()
	if other := imageSchedulers.owners[s.store]; other != nil && other != q {
		imageSchedulers.Unlock()
		return &ImageError{Code: CodeUnavailable}
	}
	imageSchedulers.owners[s.store] = q
	imageSchedulers.Unlock()
	s.mu.Lock()
	if s.scheduler != nil && s.scheduler != q {
		s.mu.Unlock()
		q.releaseOwnership()
		return &ImageError{Code: CodeUnavailable}
	}
	s.scheduler = q
	s.mu.Unlock()
	if err := q.recover(ctx); err != nil {
		q.releaseOwnership()
		return err
	}
	run, cancel := context.WithCancel(ctx)
	q.cancel = cancel
	q.started = true
	go q.loop(run)
	return nil
}
func (q *Scheduler) Close(ctx context.Context) error {
	q.service.StopAccepting()
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		if q.cancel != nil {
			q.cancel()
		} else {
			close(q.done)
		}
	}
	done := q.done
	q.mu.Unlock()
	select {
	case <-ctx.Done():
		select {
		case <-done:
		default:
			return ctx.Err()
		}
	case <-done:
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

// Done closes after all workers have exited, even when Close reports an error.
func (q *Scheduler) Done() <-chan struct{} { return q.done }
func (q *Scheduler) fail(err error) {
	q.mu.Lock()
	if q.err == nil {
		q.err = err
	}
	q.mu.Unlock()
	q.service.StopAccepting()
}
func (q *Scheduler) releaseOwnership() {
	imageSchedulers.Lock()
	if imageSchedulers.owners[q.service.store] == q {
		delete(imageSchedulers.owners, q.service.store)
	}
	imageSchedulers.Unlock()
}
func (q *Scheduler) loop(ctx context.Context) {
	defer close(q.done)
	defer q.releaseOwnership()
	defer q.service.StopAccepting()
	s := q.service
	renew := s.clock.After(s.options.LeaseRenewInterval)
	for {
		if ctx.Err() != nil {
			for len(q.running) > 0 {
				key := <-q.finished
				delete(q.running, key)
			}
			return
		}
		works := s.executionWork()
		active := map[string]int{}
		network := map[string]int{}
		for _, running := range q.running {
			network[running.group()]++
		}
		for _, w := range works {
			_, reserved := q.running[w.key()]
			item := w.job.Items[w.index]
			if reserved || (!terminalJob(item.Status) && item.Status != JobQueued && len(w.execution.Items[item.ID].Attempts) > 0) {
				active[w.group()]++
			}
		}
		sort.SliceStable(works, func(i, j int) bool {
			if works[i].group() != works[j].group() {
				return works[i].group() < works[j].group()
			}
			if !works[i].job.CreatedAt.Equal(works[j].job.CreatedAt) {
				return works[i].job.CreatedAt.Before(works[j].job.CreatedAt)
			}
			return works[i].key() < works[j].key()
		})
		now := s.clock.Now()
		delay := time.Second
		for len(q.running) < s.options.GlobalConcurrency {
			picked := -1
			for pass := 0; pass < 2 && picked < 0; pass++ {
				for n, w := range works {
					if pass == 0 && w.group() <= q.lastGroup {
						continue
					}
					if _, ok := q.running[w.key()]; ok {
						continue
					}
					item := w.job.Items[w.index]
					if terminalJob(w.job.Status) || terminalJob(item.Status) {
						continue
					}
					x := w.execution.Items[item.ID]
					if d := w.execution.Deadline.Sub(now); d > 0 && d < delay {
						delay = d
					}
					if w.job.Status != JobCancelRequested && now.Before(x.NextPollAt) && now.Before(w.execution.Deadline) {
						if d := x.NextPollAt.Sub(now); d < delay {
							delay = d
						}
						continue
					}
					if !x.LeaseExpiresAt.IsZero() && x.LeaseOwner != q.owner && now.Before(x.LeaseExpiresAt) {
						continue
					}
					limit := s.connectionLimit(w.scope, w.job.TargetSnapshot.ConnectionID)
					if network[w.group()] >= limit || (item.Status == JobQueued && active[w.group()] >= limit) {
						continue
					}
					picked = n
					break
				}
			}
			if picked < 0 {
				break
			}
			w := works[picked]
			q.lastGroup = w.group()
			if w.job.Items[w.index].Status == JobQueued {
				active[w.group()]++
			}
			q.running[w.key()] = w
			network[w.group()]++
			go func() {
				defer func() { q.finished <- w.key() }()
				if err := q.execute(ctx, w); err != nil {
					q.fail(err)
				}
			}()
		}
		select {
		case <-ctx.Done():
		case key := <-q.finished:
			delete(q.running, key)
		case <-s.wake:
		case <-s.clock.After(delay):
		case <-renew:
			if err := q.renew(); err != nil {
				q.fail(err)
			}
			renew = s.clock.After(s.options.LeaseRenewInterval)
		}
		q.mu.Lock()
		failed := q.err != nil
		q.mu.Unlock()
		if failed {
			q.cancel()
		}
	}
}
func (s *Service) connectionLimit(scope Scope, id string) int {
	if s.options.ConnectionConcurrency != nil {
		n := s.options.ConnectionConcurrency(scope, id)
		if n >= 1 && n <= 16 {
			return n
		}
	}
	return 2
}

// Snapshot internal execution envelopes without exposing owner or handles through wire APIs.
func (s *Service) executionWork() []engineWork {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	var out []engineWork
	for id, r := range s.store.data.Jobs {
		if r.DeletedAt != nil {
			continue
		}
		d, ok := s.store.data.Executions[id]
		if !ok {
			continue
		}
		j := cloneStoreValue(r.Value)
		ex := executionFromDisk(d)
		scope := Scope{OwnerID: r.Scope.OwnerID, BackendID: r.Scope.BackendID}
		for i := range j.Items {
			out = append(out, engineWork{scope: scope, job: j, execution: ex, index: i})
		}
	}
	return out
}
func (q *Scheduler) renew() error {
	s := q.service
	for _, w := range q.running {
		err := s.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
			ex, err := tx.ReadExecution(w.scope, w.job.ID)
			if err != nil {
				// A completed worker can release its lease and allow deletion
				// before the dispatcher consumes its finished notification.
				var imageErr *ImageError
				if errors.As(err, &imageErr) && imageErr.Code == CodeNotFound && tx.data.Jobs[w.job.ID].DeletedAt != nil {
					return nil
				}
				return err
			}
			id := w.job.Items[w.index].ID
			x := ex.Items[id]
			if x.LeaseOwner != q.owner {
				return nil
			}
			x.LeaseExpiresAt = s.clock.Now().Add(s.options.LeaseDuration)
			ex.Items[id] = x
			return tx.PutExecution(ex)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func (q *Scheduler) releaseLease(w engineWork) error {
	return q.service.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
		ex, err := tx.ReadExecution(w.scope, w.job.ID)
		if err != nil {
			return err
		}
		id := w.job.Items[w.index].ID
		x := ex.Items[id]
		if x.LeaseOwner != q.owner {
			return nil
		}
		x.LeaseOwner = ""
		x.LeaseExpiresAt = time.Time{}
		ex.Items[id] = x
		return tx.PutExecution(ex)
	})
}

func (q *Scheduler) execute(ctx context.Context, w engineWork) (executeErr error) {
	s := q.service
	id := w.job.Items[w.index].ID
	claimed := false
	// The claim transaction re-reads cancellation and records submitting before any I/O.
	err := s.store.Update(WithScope(ctx, w.scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(w.scope, w.job.ID)
		if err != nil {
			// The snapshot may have been cancelled and deleted before this claim.
			var imageErr *ImageError
			if errors.As(err, &imageErr) && imageErr.Code == CodeNotFound && tx.data.Jobs[w.job.ID].DeletedAt != nil {
				return nil
			}
			return err
		}
		ex, err := tx.ReadExecution(w.scope, j.ID)
		if err != nil {
			return err
		}
		i := &j.Items[w.index]
		x := ex.Items[id]
		w.pollDelay = x.NextPollAt.Sub(j.UpdatedAt)
		if terminalJob(j.Status) || terminalJob(i.Status) {
			return nil
		}
		now := s.clock.Now()
		if !ex.Deadline.IsZero() && !now.Before(ex.Deadline) {
			i.Status = JobResultUnknown
			i.Error = &ImageError{Code: CodeResultUnknown, Reason: "job deadline passed before the provider reported a result"}
			if len(x.Attempts) == 0 {
				i.Status = JobFailed
				i.Error = &ImageError{Code: CodeUnavailable, Reason: "job deadline passed before any attempt started"}
			}
			aggregateJob(&j, ex)
			return s.writeJob(tx, &j, jobEvent(j.Status))
		}
		if x.LeaseOwner != "" && x.LeaseOwner != q.owner && now.Before(x.LeaseExpiresAt) {
			return nil
		}
		switch {
		case j.Status == JobCancelRequested:
			w.kind = "cancel"
		case x.ProviderHandle != "":
			w.kind = "poll"
		case i.Status == JobQueued:
			w.kind = "submit"
		default:
			w.kind = "reconcile"
		}
		x.LeaseOwner = q.owner
		x.LeaseExpiresAt = now.Add(s.options.LeaseDuration)
		if w.kind == "submit" {
			x.Attempts = append(x.Attempts, ImageAttempt{AttemptNo: len(x.Attempts) + 1, StartedAt: now, SubmissionState: "submitting"})
			i.AttemptCount = len(x.Attempts)
			i.Status = JobRunning
			i.Error = nil // the last attempt's reason says nothing about this one
			j.Status = JobRunning
		}
		ex.Items[id] = x
		if err = tx.PutExecution(ex); err != nil {
			return err
		}
		if err = s.writeJob(tx, &j, EventJobStateChanged); err != nil {
			return err
		}
		w.job = j
		w.execution = ex
		claimed = true
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if !claimed {
		return nil
	}
	// Keep the durable lease through output publication and every finish path.
	// Cancellation and shutdown may settle status before the worker stops using assets.
	defer func() { executeErr = errors.Join(executeErr, q.releaseLease(w)) }()
	duration := s.options.SubmitTimeout
	if d := w.execution.Deadline.Sub(s.clock.Now()); d < duration {
		duration = d
	}
	call, cancel := s.timedContext(ctx, duration)
	defer cancel()
	var a Adapter
	if s.options.AdapterResolver == nil {
		err = ErrUnsupported
	} else {
		a, err = s.options.AdapterResolver(call, w.scope, w.job.TargetSnapshot)
	}
	if err != nil {
		return q.finish(ctx, w, RemoteResult{}, &AdapterError{Code: safeEngineError(err).Code, Delivery: DeliveryRejected, Cause: err, Reason: "image connection could not be prepared"})
	}
	x := w.execution.Items[id]
	var result RemoteResult
	switch w.kind {
	case "cancel":
		if x.ProviderHandle == "" {
			err = ErrUnsupported
		} else {
			err = a.Cancel(call, x.ProviderHandle)
		}
		return q.finishCancel(w, err)
	case "poll":
		result, err = a.Poll(call, x.ProviderHandle)
	case "reconcile":
		if !w.job.TargetSnapshot.Capabilities.SupportsReconcile {
			err = ErrUnsupported
		} else {
			result, err = a.Reconcile(call, x.SubmissionKey)
		}
	case "submit":
		request := cloneRequest(w.job.Request)
		request.Count = 1
		for _, p := range w.job.TargetSnapshot.Parameters {
			if _, ok := request.Parameters[p.Key]; !ok && p.Default != nil {
				request.Parameters[p.Key] = cloneValue(p.Default)
			}
		}
		var inputs []ResolvedAsset
		for _, input := range request.Inputs {
			var asset ResolvedAsset
			if s.options.InputResolver == nil {
				err = ErrUnsupported
			} else {
				asset, err = s.options.InputResolver.ResolveAsset(w.scope, input.AssetID)
			}
			if err != nil {
				return q.finish(ctx, w, RemoteResult{}, &AdapterError{Code: safeEngineError(err).Code, Delivery: DeliveryRejected, Cause: err, Reason: "input image could not be loaded"})
			}
			inputs = append(inputs, asset)
		}
		result, err = a.Submit(call, ExecuteRequest{ItemID: id, Target: cloneStoreValue(w.job.TargetSnapshot), Request: request, Inputs: inputs, SubmissionKey: x.SubmissionKey, Continuation: append([]byte(nil), x.Continuation...)})
	}
	// Shutdown leaves the durable submitting/pending record for reconciliation.
	if ctx.Err() != nil && err != nil {
		return nil
	}
	// The worker is alive, so a finished call context means the submit timer fired. The
	// transport reports that as a plain cancellation, which would read as if someone cancelled.
	var timedOut *AdapterError
	if err != nil && call.Err() != nil && errors.As(err, &timedOut) && timedOut.Delivery == DeliveryUncertain && timedOut.Code == CodeResultUnknown {
		timedOut.Reason = "provider did not respond within " + strconv.Itoa(int(duration.Seconds())) + " seconds"
	}
	return q.finish(ctx, w, result, err)
}
func jobEvent(status JobStatus) string {
	if terminalJob(status) {
		return EventJobFinished
	}
	return EventJobStateChanged
}
func (q *Scheduler) finishCancel(w engineWork, cancelErr error) error {
	s := q.service
	return s.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(w.scope, w.job.ID)
		if err != nil {
			return err
		}
		if terminalJob(j.Status) {
			return nil
		}
		ex, err := tx.ReadExecution(w.scope, j.ID)
		if err != nil {
			return err
		}
		j.Items[w.index].Status = JobCancelled
		if cancelErr != nil {
			j.CancelEffect = "local_only"
		} else if j.CancelEffect != "local_only" {
			j.CancelEffect = "upstream_confirmed"
		}
		aggregateJob(&j, ex)
		if err = tx.PutExecution(ex); err != nil {
			return err
		}
		return s.writeJob(tx, &j, jobEvent(j.Status))
	})
}
func (q *Scheduler) finish(ctx context.Context, w engineWork, result RemoteResult, callErr error) error {
	s := q.service
	if len(result.Outputs) > 0 {
		var cancel context.CancelFunc
		ctx, cancel = s.timedContext(ctx, w.execution.Deadline.Sub(s.clock.Now()))
		defer cancel()
	}
	id := w.job.Items[w.index].ID
	// Save each output independently. Publish each saved file with its references in
	// one transaction; a later failed output cannot erase an earlier success.
	saved, saveFailed := 0, false
	var saveError *ImageError
	if len(result.Outputs) > 0 {
		if err := s.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
			j, err := tx.ReadJob(w.scope, w.job.ID)
			if err != nil {
				return err
			}
			ex, err := tx.ReadExecution(w.scope, j.ID)
			if err != nil {
				return err
			}
			x := ex.Items[id]
			if result.Handle != "" {
				x.ProviderHandle = result.Handle
			}
			if len(x.Attempts) > 0 {
				x.Attempts[len(x.Attempts)-1].SubmissionState = "saving"
				if result.Usage != nil {
					x.Attempts[len(x.Attempts)-1].Usage = cloneUsage(result.Usage)
				}
			}
			ex.Items[id] = x
			if err = tx.PutExecution(ex); err != nil {
				return err
			}
			if terminalJob(j.Status) {
				return nil
			}
			j.Items[w.index].Status = JobSaving
			if j.Status != JobCancelRequested {
				j.Status = JobSaving
			}
			return s.writeJob(tx, &j, EventJobStateChanged)
		}); err != nil {
			return err
		}
		for _, output := range result.Outputs {
			var asset ImageAsset
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				if s.options.AssetSink == nil {
					err = ErrUnsupported
					break
				}
				if !s.clock.Now().Before(w.execution.Deadline) {
					err = context.DeadlineExceeded
					break
				}
				if err = s.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
					ex, e := tx.ReadExecution(w.scope, w.job.ID)
					if e != nil {
						return e
					}
					x := ex.Items[id]
					x.DownloadAttempts++
					ex.Items[id] = x
					return tx.PutExecution(ex)
				}); err != nil {
					return err
				}
				asset, err = s.options.AssetSink.SaveOutput(ctx, w.scope, output)
				if err == nil {
					break
				}
				var spent downloadBudgetSpent
				if errors.Is(err, ErrUnsupported) || ctx.Err() != nil || errors.As(err, &spent) {
					break
				}
				if attempt < 2 {
					if e := s.waitRetry(ctx, s.backoff(attempt+1, 0)); e != nil {
						break
					}
				}
			}
			if err != nil || asset.ID == "" {
				saveFailed = true
				// The one failure with a remedy for the user stays, whichever output came last.
				if saveError != nil && slowDirectDownload(saveError) {
					continue
				}
				saveError = outputSaveError(err)
				// The deadline ends a download through a plain cancel; nobody cancelled it.
				if !slowDirectDownload(saveError) && !w.execution.Deadline.IsZero() && !s.clock.Now().Before(w.execution.Deadline) {
					saveError.Reason = "job deadline passed while saving the generated image"
				}
				continue
			}
			asset.JobID = w.job.ID
			asset.Source = "generated"
			asset.ParentAssetIDs = nil
			for _, input := range w.job.Request.Inputs {
				asset.ParentAssetIDs = append(asset.ParentAssetIDs, input.AssetID)
			}
			if err = s.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
				j, e := tx.ReadJob(w.scope, w.job.ID)
				if e != nil {
					return e
				}
				if w.kind != "submit" && asset.Hash != "" {
					for _, existingID := range j.Items[w.index].OutputAssetIDs {
						existing, readErr := tx.ReadAsset(w.scope, existingID)
						if readErr != nil {
							return readErr
						}
						if existing.Hash == asset.Hash {
							return nil
						}
					}
				}
				if e = tx.PutAsset(asset); e != nil {
					return e
				}
				j.AssetIDs = append(j.AssetIDs, asset.ID)
				j.Items[w.index].OutputAssetIDs = append(j.Items[w.index].OutputAssetIDs, asset.ID)
				j.Revision++
				j.UpdatedAt = s.clock.Now().UTC()
				if e = tx.PutJob(j); e != nil {
					return e
				}
				return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventAssetReady, Timestamp: j.UpdatedAt, Data: ImageEventData{ItemID: id, Asset: &asset}})
			}); err != nil {
				return err
			}
			saved++
		}
	}
	// A shutdown during saving leaves the recoverable record and any published
	// partial assets intact. A job deadline, however, must settle locally.
	if ctx.Err() != nil && saveFailed && s.clock.Now().Before(w.execution.Deadline) {
		return nil
	}
	return s.store.Update(WithScope(context.Background(), w.scope), func(tx *Transaction) error {
		j, err := tx.ReadJob(w.scope, w.job.ID)
		if err != nil {
			return err
		}
		ex, err := tx.ReadExecution(w.scope, j.ID)
		if err != nil {
			return err
		}
		item := &j.Items[w.index]
		x := ex.Items[id]
		if result.Handle != "" {
			x.ProviderHandle = result.Handle
		}
		if len(x.Attempts) > 0 {
			a := &x.Attempts[len(x.Attempts)-1]
			a.ProviderHandle = x.ProviderHandle
			if result.Usage != nil {
				a.Usage = cloneUsage(result.Usage)
			}
		}
		if terminalJob(j.Status) {
			ex.Items[id] = x
			return tx.PutExecution(ex)
		}
		item.Error = nil
		if callErr != nil {
			item.Error = safeEngineError(callErr)
			var ae *AdapterError
			isAdapter := errors.As(callErr, &ae)
			if w.kind == "submit" {
				delivery := DeliveryUncertain
				if isAdapter && ae.Delivery.Valid() {
					delivery = ae.Delivery
				}
				if len(x.Attempts) > 0 {
					x.Attempts[len(x.Attempts)-1].Delivery = delivery
					x.Attempts[len(x.Attempts)-1].Error = item.Error
				}
				switch delivery {
				case DeliveryNotSent:
					if len(x.Attempts) < 3 {
						item.Status = JobQueued
						x.NextPollAt = s.clock.Now().Add(s.backoff(len(x.Attempts), ae.RetryAfter))
					} else {
						item.Status = JobFailed
					}
				case DeliveryRejected:
					item.Status = JobFailed
				default:
					item.Status = JobRunning
					x.NextPollAt = s.clock.Now().Add(s.backoff(1, 0))
				}
			} else {
				x.QueryFailures++
				if item.Error.Code == CodeAuthRequired || item.Error.Code == CodeForbidden {
					item.Status = JobResultUnknown
				} else if errors.Is(callErr, ErrUnsupported) || x.QueryFailures >= 5 {
					// An unsupported lookup says nothing about the provider; a failed one does.
					fallback := "provider status could not be confirmed"
					if !errors.Is(callErr, ErrUnsupported) && item.Error.Reason != "" {
						fallback = item.Error.Reason
					}
					item.Status = JobResultUnknown
					item.Error = &ImageError{Code: CodeResultUnknown, Reason: lastAttemptReason(x, fallback)}
				} else {
					hint := time.Duration(0)
					if isAdapter {
						hint = ae.RetryAfter
					}
					item.Status = JobRunning
					x.NextPollAt = s.clock.Now().Add(s.backoff(x.QueryFailures, hint))
				}
			}
		} else {
			x.QueryFailures = 0
			switch result.State {
			case "pending":
				if x.ProviderHandle == "" {
					item.Status = JobResultUnknown
					item.Error = &ImageError{Code: CodeResultUnknown, Reason: "provider gave no task handle to look up"}
				} else {
					item.Status = JobRunning
					d := s.backoff(1, 0)
					if w.kind == "poll" && w.pollDelay > 0 {
						d = min(w.pollDelay*2, s.options.MaxPollInterval)
					}
					if result.NextPollAfter > d {
						d = result.NextPollAfter
					}
					x.NextPollAt = s.clock.Now().Add(d)
				}
			case "completed":
				if saved > 0 || len(item.OutputAssetIDs) > 0 {
					item.Status = JobSucceeded
					if saveFailed {
						item.Status = JobPartialSucceeded
						item.Error = saveError
					}
				} else {
					item.Status = JobFailed
					item.Error = &ImageError{Code: CodeNoImageOutput, Reason: "provider reported success but returned no image"}
					if saveFailed {
						item.Error = saveError
					}
				}
			case "failed":
				item.Status = JobFailed
				item.Error = PublicError(&ImageError{Code: result.ErrorCode, Reason: result.ErrorReason})
			case "unknown":
				// Adapters report this on purpose when a provider cannot say how a task ended.
				item.Status = JobResultUnknown
				reason := result.ErrorReason
				if reason == "" {
					reason = lastAttemptReason(x, "provider could not confirm the result")
				}
				item.Error = &ImageError{Code: CodeResultUnknown, Reason: reason}
			default:
				item.Status = JobResultUnknown
				item.Error = &ImageError{Code: CodeResultUnknown, Reason: "provider returned an unrecognised state"}
			}
		}
		if saved > 0 && item.Status == JobFailed {
			item.Status = JobPartialSucceeded
		}
		if len(x.Attempts) > 0 {
			x.Attempts[len(x.Attempts)-1].SubmissionState = string(item.Status)
		}
		ex.Items[id] = x
		aggregateJob(&j, ex)
		if err = tx.PutExecution(ex); err != nil {
			return err
		}
		return s.writeJob(tx, &j, jobEvent(j.Status))
	})
}
