package usecase

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/validations"
)

func (s *serviceRuntime) scheduleLoop() {
	defer s.wg.Done()
	var cursor scheduleCursor
	for s.ctx.Err() == nil {
		var err error
		cursor, err = s.materializeDueSchedules(time.Now().UTC(), cursor)
		s.workerError(err)
		if !s.wait() {
			return
		}
	}
}

type scheduleCursor struct {
	nextAt time.Time
	id     string
}

// One poll attempts at most 100 occurrences. Progress is independent of queue
// admission, and a wrap performs at most one additional bounded read. This is
// transient scheduler state: restarting safely starts again from the oldest row.
func (s *serviceRuntime) materializeDueSchedules(at time.Time, cursor scheduleCursor) (scheduleCursor, error) {
	const batchSize = 100
	jobs, err := s.scheduleStore.DueSchedules(s.ctx, at, batchSize, cursor.nextAt, cursor.id)
	if err != nil {
		return cursor, err
	}
	if len(jobs) == 0 && cursor.id != "" {
		jobs, err = s.scheduleStore.DueSchedules(s.ctx, at, batchSize, time.Time{}, "")
		if err != nil {
			return cursor, err
		}
	}
	for _, job := range jobs {
		if err := s.ctx.Err(); err != nil {
			return cursor, err
		}
		s.materializeSchedule(job)
		cursor = scheduleCursor{nextAt: job.NextAt, id: job.ID}
	}
	if len(jobs) < batchSize {
		cursor = scheduleCursor{}
	}
	return cursor, nil
}

func (s *serviceRuntime) materializeSchedule(job domains.Schedule) {
	// Match creation-time parsing, including schedules already persisted with
	// surrounding whitespace. Keep the stored request unchanged: its original
	// representation is part of the existing idempotency contract.
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(job.Request.ScheduledAt))
	if err != nil {
		_ = s.scheduleStore.SetScheduleState(s.ctx, job.ConnectionID, job.ID, "failed")
		return
	}
	// Validation uses the original creation-time boundary; later occurrences can
	// legitimately be processed after the original scheduled_at.
	spec, err := validations.ParseScheduleOptions(job.Request.ScheduleOptions, start.Add(-time.Nanosecond))
	if err != nil {
		_ = s.scheduleStore.SetScheduleState(s.ctx, job.ConnectionID, job.ID, "failed")
		return
	}
	now := time.Now().UTC()
	if (spec.EndAt != nil && now.After(*spec.EndAt)) || (spec.OccurrenceLimit > 0 && job.Count >= spec.OccurrenceLimit) {
		_ = s.scheduleStore.SetScheduleState(s.ctx, job.ConnectionID, job.ID, "completed")
		return
	}
	var next *time.Time
	if spec.OccurrenceLimit == 0 || job.Count+1 < spec.OccurrenceLimit {
		after := job.NextAt
		if now.After(after) {
			after = now
		}
		if candidate, ok := validations.NextScheduleOccurrence(spec, after); ok {
			next = &candidate
		}
	}
	// Storage atomically enqueues an occurrence with an authoritative occurrence record and
	// advances this schedule. A restart between these actions cannot duplicate it.
	_, err = s.scheduleStore.MaterializeSchedule(s.ctx, job.ConnectionID, job.ID, job.NextAt, next, s.admissionLimits())
	s.workerError(err)
}
func eventTypeFromBody(body json.RawMessage) string {
	var event struct {
		Type string `json:"event"`
	}
	_ = json.Unmarshal(body, &event)
	return event.Type
}
