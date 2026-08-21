package store

import "thermoguard/internal/domain"

type State struct {
	SchemaVersion  int                                    `json:"schema_version"`
	Revision       int64                                  `json:"revision"`
	NextID         int64                                  `json:"next_id"`
	Policies       map[string]domain.TemperaturePolicy    `json:"policies"`
	Lots           map[string]domain.MonitoredLot         `json:"lots"`
	Readings       map[string][]domain.TemperatureReading `json:"readings"`
	Excursions     map[string][]domain.Excursion          `json:"excursions"`
	Investigations map[string]domain.Investigation        `json:"investigations"`
	Evidence       map[string][]domain.Evidence           `json:"evidence"`
	Actions        map[string]domain.CAPAAction           `json:"actions"`
	Decisions      map[string][]domain.ReleaseDecision    `json:"decisions"`
	Audit          []domain.AuditEvent                    `json:"audit"`
	Idempotency    map[string]domain.IdempotencyRecord    `json:"idempotency"`
}

func NewState() State {
	return State{SchemaVersion: domain.SchemaVersion, NextID: 1,
		Policies: map[string]domain.TemperaturePolicy{}, Lots: map[string]domain.MonitoredLot{},
		Readings: map[string][]domain.TemperatureReading{}, Excursions: map[string][]domain.Excursion{},
		Investigations: map[string]domain.Investigation{}, Evidence: map[string][]domain.Evidence{},
		Actions: map[string]domain.CAPAAction{}, Decisions: map[string][]domain.ReleaseDecision{},
		Idempotency: map[string]domain.IdempotencyRecord{}}
}

func (s *State) Normalize() {
	if s.Policies == nil {
		s.Policies = map[string]domain.TemperaturePolicy{}
	}
	if s.Lots == nil {
		s.Lots = map[string]domain.MonitoredLot{}
	}
	if s.Readings == nil {
		s.Readings = map[string][]domain.TemperatureReading{}
	}
	if s.Excursions == nil {
		s.Excursions = map[string][]domain.Excursion{}
	}
	if s.Investigations == nil {
		s.Investigations = map[string]domain.Investigation{}
	}
	if s.Evidence == nil {
		s.Evidence = map[string][]domain.Evidence{}
	}
	if s.Actions == nil {
		s.Actions = map[string]domain.CAPAAction{}
	}
	if s.Decisions == nil {
		s.Decisions = map[string][]domain.ReleaseDecision{}
	}
	if s.Idempotency == nil {
		s.Idempotency = map[string]domain.IdempotencyRecord{}
	}
	if s.NextID == 0 {
		s.NextID = 1
	}
}
