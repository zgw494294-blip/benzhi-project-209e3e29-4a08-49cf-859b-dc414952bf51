package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"thermoguard/internal/clock"
	"thermoguard/internal/domain"
	"thermoguard/internal/store"
)

type Service struct {
	store           *store.Store
	clock           clock.Clock
	enqueueEvaluate func(string) bool
}

func New(s *store.Store, c clock.Clock) *Service { return &Service{store: s, clock: c} }
func (s *Service) Store() *store.Store           { return s.store }

// SetEvaluateEnqueuer 在启动装配阶段连接有界后台评估器。
// 前台事务仍会立即评估，确保读接口始终返回最新结果。
func (s *Service) SetEvaluateEnqueuer(enqueue func(string) bool) { s.enqueueEvaluate = enqueue }

type CreatePolicyInput struct {
	Name              string  `json:"name"`
	MinC              float64 `json:"min_c"`
	MaxC              float64 `json:"max_c"`
	MaxGapMinutes     int     `json:"max_gap_minutes"`
	ContinuousMinutes int     `json:"continuous_minutes"`
	CumulativeMinutes int     `json:"cumulative_minutes"`
	MajorDeltaC       float64 `json:"major_delta_c"`
	CriticalDeltaC    float64 `json:"critical_delta_c"`
}

func (s *Service) CreatePolicy(actor string, in CreatePolicyInput) (domain.TemperaturePolicy, error) {
	p := domain.TemperaturePolicy{Name: strings.TrimSpace(in.Name), Version: 1, Status: domain.PolicyDraft, MinC: in.MinC, MaxC: in.MaxC, MaxGapMinutes: in.MaxGapMinutes, ContinuousMinutes: in.ContinuousMinutes, CumulativeMinutes: in.CumulativeMinutes, MajorDeltaC: in.MajorDeltaC, CriticalDeltaC: in.CriticalDeltaC, CreatedAt: s.clock.Now()}
	if err := domain.ValidatePolicy(p); err != nil {
		return p, err
	}
	err := s.store.Update(actor, "POLICY_CREATED", "policy", "pending", "", "", p.Name, func(st *store.State) error { p.ID = s.store.NextID(st, "pol"); st.Policies[p.ID] = p; return nil })
	return p, err
}

func (s *Service) PublishPolicy(actor, id string) (domain.TemperaturePolicy, error) {
	var result domain.TemperaturePolicy
	err := s.store.Update(actor, "POLICY_PUBLISHED", "policy", id, "", "DRAFT", "PUBLISHED", func(st *store.State) error {
		p, ok := st.Policies[id]
		if !ok {
			return domain.NotFound("规则", id)
		}
		if p.Status != domain.PolicyDraft {
			return domain.StateConflict("规则已经发布")
		}
		if err := domain.ValidatePolicy(p); err != nil {
			return err
		}
		now := s.clock.Now()
		p.PublishedAt = &now
		p.Status = domain.PolicyPublished
		st.Policies[id] = p
		result = p
		return nil
	})
	return result, err
}

func (s *Service) GetPolicy(id string) (domain.TemperaturePolicy, error) {
	var result domain.TemperaturePolicy
	err := s.store.View(func(st store.State) error {
		var ok bool
		result, ok = st.Policies[id]
		if !ok {
			return domain.NotFound("规则", id)
		}
		return nil
	})
	return result, err
}

type CreateLotInput struct {
	Code     string    `json:"code"`
	Product  string    `json:"product"`
	PolicyID string    `json:"policy_id"`
	StartAt  time.Time `json:"start_at"`
	EndAt    time.Time `json:"end_at"`
}

func (s *Service) CreateLot(actor string, in CreateLotInput) (domain.MonitoredLot, error) {
	lot := domain.MonitoredLot{Code: strings.TrimSpace(in.Code), Product: strings.TrimSpace(in.Product), PolicyID: in.PolicyID, StartAt: in.StartAt, EndAt: in.EndAt, Status: domain.LotMonitoring, Version: 1, CreatedAt: s.clock.Now()}
	if err := domain.ValidateLot(lot); err != nil {
		return lot, err
	}
	lot.StartAt = lot.StartAt.UTC()
	lot.EndAt = lot.EndAt.UTC()
	err := s.store.Update(actor, "LOT_CREATED", "lot", "pending", "", "", lot.Code, func(st *store.State) error {
		p, ok := st.Policies[in.PolicyID]
		if !ok {
			return domain.NotFound("规则", in.PolicyID)
		}
		if p.Status != domain.PolicyPublished {
			return domain.StateConflict("批次只能绑定已发布规则")
		}
		for _, existing := range st.Lots {
			if existing.Code == lot.Code {
				return domain.Conflict("批次编码已存在")
			}
		}
		lot.ID = s.store.NextID(st, "lot")
		st.Lots[lot.ID] = lot
		return nil
	})
	return lot, err
}

func (s *Service) GetLot(id string) (domain.MonitoredLot, domain.ReleasePreview, error) {
	var lot domain.MonitoredLot
	var preview domain.ReleasePreview
	err := s.store.View(func(st store.State) error {
		var ok bool
		lot, ok = st.Lots[id]
		if !ok {
			return domain.NotFound("批次", id)
		}
		preview = releasePreview(st, lot, s.clock.Now())
		return nil
	})
	return lot, preview, err
}

func (s *Service) ListLots(status domain.LotStatus, policyID, cursor string, limit int) ([]domain.MonitoredLot, string, error) {
	if status != "" && status != domain.LotDraft && status != domain.LotMonitoring && status != domain.LotUnderReview && status != domain.LotReleased && status != domain.LotRejected {
		return nil, "", domain.Invalid("status", "批次状态筛选值无效")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		return nil, "", domain.Invalid("limit", "分页大小不能超过 200")
	}
	var result []domain.MonitoredLot
	next := ""
	err := s.store.View(func(st store.State) error {
		for _, lot := range st.Lots {
			if status != "" && lot.Status != status {
				continue
			}
			if policyID != "" && lot.PolicyID != policyID {
				continue
			}
			if cursor != "" && lot.ID <= cursor {
				continue
			}
			result = append(result, lot)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		if len(result) > limit {
			next = result[limit-1].ID
			result = result[:limit]
		}
		return nil
	})
	return result, next, err
}

type AddReadingInput struct {
	DeviceID       string    `json:"device_id"`
	SampledAt      time.Time `json:"sampled_at"`
	Celsius        float64   `json:"celsius"`
	Source         string    `json:"source"`
	IdempotencyKey string    `json:"idempotency_key"`
}
type ReadingResult struct {
	Reading   domain.TemperatureReading `json:"reading"`
	Duplicate bool                      `json:"duplicate"`
}

type ReadingValidation struct {
	Total          int `json:"total"`
	NewCount       int `json:"new_count"`
	DuplicateCount int `json:"duplicate_count"`
}

func (s *Service) ValidateReadings(lotID string, inputs []AddReadingInput) (ReadingValidation, error) {
	result := ReadingValidation{Total: len(inputs)}
	if len(inputs) == 0 || len(inputs) > 1000 {
		return result, domain.Invalid("readings", "批量读数数量必须在 1 到 1000 之间")
	}
	err := s.store.View(func(st store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		if lot.Status != domain.LotMonitoring {
			return domain.StateConflict("只有监测中的批次可以接收读数")
		}
		seen := make(map[string]string, len(inputs))
		for _, input := range inputs {
			reading := domain.TemperatureReading{
				LotID: lotID, DeviceID: strings.TrimSpace(input.DeviceID), SampledAt: input.SampledAt,
				Celsius: input.Celsius, Source: strings.TrimSpace(input.Source), IdempotencyKey: input.IdempotencyKey,
			}
			if err := domain.ValidateReading(lot, reading); err != nil {
				return err
			}
			reading.SampledAt = reading.SampledAt.UTC()
			digest := digestJSON(reading.DeviceID, reading.SampledAt, reading.Celsius, reading.Source)
			key := lotID + "/reading/" + reading.IdempotencyKey
			if previous, exists := seen[key]; exists {
				if previous != digest {
					return domain.Conflict("同一批请求中的幂等键对应不同内容")
				}
				return domain.Conflict("同一批请求包含重复幂等键")
			}
			seen[key] = digest
			if persisted, exists := st.Idempotency[key]; exists {
				if persisted.Digest != digest {
					return domain.Conflict("幂等键已用于不同内容")
				}
				result.DuplicateCount++
			} else {
				result.NewCount++
			}
		}
		return nil
	})
	return result, err
}

func (s *Service) AddReading(actor, lotID string, in AddReadingInput) (ReadingResult, error) {
	results, err := s.AddReadings(actor, lotID, []AddReadingInput{in})
	if err != nil {
		return ReadingResult{}, err
	}
	return results[0], nil
}

func (s *Service) AddReadings(actor, lotID string, inputs []AddReadingInput) ([]ReadingResult, error) {
	if len(inputs) == 0 || len(inputs) > 1000 {
		return nil, domain.Invalid("readings", "批量读数数量必须在 1 到 1000 之间")
	}
	results := make([]ReadingResult, len(inputs))
	createdAny := false
	err := s.store.Update(actor, "READINGS_ADDED", "lot", lotID, lotID, "", fmt.Sprintf("%d 条读数", len(inputs)), func(st *store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		if lot.Status != domain.LotMonitoring {
			return domain.StateConflict("只有监测中的批次可以接收读数")
		}
		seen := map[string]bool{}
		created := false
		for i, in := range inputs {
			r := domain.TemperatureReading{LotID: lotID, DeviceID: strings.TrimSpace(in.DeviceID), SampledAt: in.SampledAt, Celsius: in.Celsius, Source: strings.TrimSpace(in.Source), IdempotencyKey: in.IdempotencyKey, CreatedAt: s.clock.Now()}
			if err := domain.ValidateReading(lot, r); err != nil {
				return err
			}
			r.SampledAt = r.SampledAt.UTC()
			scope := lotID + "/reading"
			key := scope + "/" + r.IdempotencyKey
			digest := digestJSON(r.DeviceID, r.SampledAt, r.Celsius, r.Source)
			if seen[key] {
				return domain.Conflict("同一批请求包含重复幂等键")
			}
			seen[key] = true
			if record, exists := st.Idempotency[key]; exists {
				if record.Digest != digest {
					return domain.Conflict("幂等键已用于不同内容")
				}
				for _, old := range st.Readings[lotID] {
					if old.ID == record.ResultID {
						results[i] = ReadingResult{Reading: old, Duplicate: true}
						break
					}
				}
				continue
			}
			r.ID = s.store.NextID(st, "rdg")
			created = true
			createdAny = true
			st.Readings[lotID] = append(st.Readings[lotID], r)
			st.Idempotency[key] = domain.IdempotencyRecord{Scope: scope, Key: r.IdempotencyKey, Digest: digest, ResultID: r.ID}
			results[i] = ReadingResult{Reading: r}
		}
		if !created {
			return store.ErrNoChange
		}
		lot.Version++
		st.Lots[lotID] = lot
		evaluateState(st, s.store, lotID, s.clock.Now())
		return nil
	})
	if err == nil && createdAny && s.enqueueEvaluate != nil {
		// 前台评估已经完成；队列任务用于统一后台重算路径，并保持结果幂等。
		_ = s.enqueueEvaluate(lotID)
	}
	return results, err
}

func (s *Service) ListReadings(lotID string, from, to *time.Time) ([]domain.TemperatureReading, error) {
	if from != nil && to != nil && from.After(*to) {
		return nil, domain.Invalid("time_range", "from 不能晚于 to")
	}
	var out []domain.TemperatureReading
	err := s.store.View(func(st store.State) error {
		if _, ok := st.Lots[lotID]; !ok {
			return domain.NotFound("批次", lotID)
		}
		for _, r := range st.Readings[lotID] {
			if from != nil && r.SampledAt.Before(*from) {
				continue
			}
			if to != nil && r.SampledAt.After(*to) {
				continue
			}
			out = append(out, r)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].SampledAt.Equal(out[j].SampledAt) {
				return out[i].ID < out[j].ID
			}
			return out[i].SampledAt.Before(out[j].SampledAt)
		})
		return nil
	})
	return out, err
}

type EvaluationSummary struct {
	Created           int   `json:"created"`
	Updated           int   `json:"updated"`
	Revoked           int   `json:"revoked"`
	EvaluationVersion int64 `json:"evaluation_version"`
}

func (s *Service) Evaluate(actor, lotID string) (EvaluationSummary, error) {
	var summary EvaluationSummary
	err := s.store.Update(actor, "LOT_EVALUATED", "lot", lotID, lotID, "", "", func(st *store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		summary = evaluateState(st, s.store, lotID, s.clock.Now())
		if summary.Created == 0 && summary.Updated == 0 && summary.Revoked == 0 && lot.EvaluationVersion == lot.Version {
			return store.ErrNoChange
		}
		return nil
	})
	return summary, err
}
func evaluateState(st *store.State, repo *store.Store, lotID string, now time.Time) EvaluationSummary {
	lot := st.Lots[lotID]
	policy := st.Policies[lot.PolicyID]
	calculated := domain.Evaluate(policy, lotID, st.Readings[lotID])
	old := st.Excursions[lotID]
	byFingerprint := map[string]domain.Excursion{}
	for _, e := range old {
		byFingerprint[e.Fingerprint] = e
	}
	active := map[string]bool{}
	summary := EvaluationSummary{}
	for i := range calculated {
		e := calculated[i]
		active[e.Fingerprint] = true
		if existing, ok := byFingerprint[e.Fingerprint]; ok {
			e.ID = existing.ID
			if excursionCalculationChanged(existing, e) || !existing.Active {
				summary.Updated++
			}
		} else {
			e.ID = repo.NextID(st, "exc")
			summary.Created++
		}
		calculated[i] = e
	}
	for _, e := range old {
		if !active[e.Fingerprint] && e.Active {
			e.Active = false
			t := now
			e.RevokedAt = &t
			calculated = append(calculated, e)
			summary.Revoked++
		} else if !active[e.Fingerprint] && !e.Active {
			calculated = append(calculated, e)
		}
	}
	sort.Slice(calculated, func(i, j int) bool { return calculated[i].ID < calculated[j].ID })
	st.Excursions[lotID] = calculated
	lot.EvaluationVersion = lot.Version
	st.Lots[lotID] = lot
	summary.EvaluationVersion = lot.EvaluationVersion
	return summary
}

func excursionCalculationChanged(old, current domain.Excursion) bool {
	return old.Type != current.Type || old.Severity != current.Severity ||
		!old.StartAt.Equal(current.StartAt) || !old.EndAt.Equal(current.EndAt) ||
		old.DurationMinutes != current.DurationMinutes || old.PeakC != current.PeakC ||
		old.ExposureDegreeMinutes != current.ExposureDegreeMinutes || old.Basis != current.Basis
}

func (s *Service) CloseMonitoring(actor, lotID string) (domain.MonitoredLot, error) {
	var result domain.MonitoredLot
	err := s.store.Update(actor, "MONITORING_CLOSED", "lot", lotID, lotID, "MONITORING", "UNDER_REVIEW", func(st *store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		if lot.Status != domain.LotMonitoring {
			return domain.StateConflict("批次不处于监测状态")
		}
		lot.Status = domain.LotUnderReview
		lot.Version++
		st.Lots[lotID] = lot
		evaluateState(st, s.store, lotID, s.clock.Now())
		result = st.Lots[lotID]
		return nil
	})
	return result, err
}

func (s *Service) ListExcursions(lotID string, includeRevoked bool) ([]domain.Excursion, error) {
	var out []domain.Excursion
	err := s.store.View(func(st store.State) error {
		if _, ok := st.Lots[lotID]; !ok {
			return domain.NotFound("批次", lotID)
		}
		for _, e := range st.Excursions[lotID] {
			if e.Active || includeRevoked {
				out = append(out, e)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return nil
	})
	return out, err
}

type CreateInvestigationInput struct {
	ExcursionIDs []string `json:"excursion_ids"`
}

func (s *Service) CreateInvestigation(actor, lotID string, in CreateInvestigationInput) (domain.Investigation, error) {
	inv := domain.Investigation{LotID: lotID, ExcursionIDs: unique(in.ExcursionIDs), Status: domain.InvestigationOpen, CreatedAt: s.clock.Now()}
	if len(inv.ExcursionIDs) == 0 {
		return inv, domain.Invalid("excursion_ids", "至少选择一个偏差")
	}
	err := s.store.Update(actor, "INVESTIGATION_OPENED", "investigation", "pending", lotID, "", strings.Join(inv.ExcursionIDs, ","), func(st *store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		if lot.Status == domain.LotReleased || lot.Status == domain.LotRejected {
			return domain.StateConflict("终态批次不能开启调查")
		}
		active := map[string]bool{}
		for _, e := range st.Excursions[lotID] {
			if e.Active {
				active[e.ID] = true
			}
		}
		for _, id := range inv.ExcursionIDs {
			if !active[id] {
				return domain.Invalid("excursion_ids", "调查只能覆盖活动偏差")
			}
		}
		inv.ID = s.store.NextID(st, "inv")
		st.Investigations[inv.ID] = inv
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lotID] = lot
		return nil
	})
	return inv, err
}

type AddEvidenceInput struct {
	Category    string    `json:"category"`
	Reference   string    `json:"reference"`
	Collector   string    `json:"collector"`
	Summary     string    `json:"summary"`
	CollectedAt time.Time `json:"collected_at"`
}

func (s *Service) AddEvidence(actor, invID string, in AddEvidenceInput) (domain.Evidence, error) {
	e := domain.Evidence{InvestigationID: invID, Category: strings.TrimSpace(in.Category), Reference: strings.TrimSpace(in.Reference), Collector: strings.TrimSpace(in.Collector), Summary: strings.TrimSpace(in.Summary), CollectedAt: in.CollectedAt}
	if e.Category == "" || e.Collector == "" || e.Summary == "" || e.CollectedAt.IsZero() || e.CollectedAt.Location() != time.UTC {
		return e, domain.Invalid("evidence", "证据类别、采集人和摘要不能为空，采集时间必须使用 UTC")
	}
	e.CollectedAt = e.CollectedAt.UTC()
	lotID, err := s.investigationLot(invID)
	if err != nil {
		return e, err
	}
	err = s.store.Update(actor, "EVIDENCE_ADDED", "evidence", "pending", lotID, "", e.Reference, func(st *store.State) error {
		inv, ok := st.Investigations[invID]
		if !ok {
			return domain.NotFound("调查", invID)
		}
		if inv.Status != domain.InvestigationOpen {
			return domain.StateConflict("只能向开放调查添加证据")
		}
		e.ID = s.store.NextID(st, "evd")
		st.Evidence[invID] = append(st.Evidence[invID], e)
		lot := st.Lots[inv.LotID]
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lot.ID] = lot
		return nil
	})
	return e, err
}

type CreateActionInput struct {
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Owner       string    `json:"owner"`
	DueAt       time.Time `json:"due_at"`
}

func (s *Service) CreateAction(actor, invID string, in CreateActionInput) (domain.CAPAAction, error) {
	a := domain.CAPAAction{InvestigationID: invID, Type: strings.ToUpper(in.Type), Description: strings.TrimSpace(in.Description), Owner: strings.TrimSpace(in.Owner), DueAt: in.DueAt, Status: domain.ActionOpen}
	if (a.Type != "CORRECTIVE" && a.Type != "PREVENTIVE") || a.Description == "" || a.Owner == "" || a.DueAt.IsZero() || a.DueAt.Location() != time.UTC {
		return a, domain.Invalid("action", "措施类型、描述和责任人不能为空，截止日必须使用 UTC")
	}
	a.DueAt = a.DueAt.UTC()
	lotID, err := s.investigationLot(invID)
	if err != nil {
		return a, err
	}
	err = s.store.Update(actor, "ACTION_CREATED", "action", "pending", lotID, "", a.Description, func(st *store.State) error {
		inv, ok := st.Investigations[invID]
		if !ok {
			return domain.NotFound("调查", invID)
		}
		if inv.Status != domain.InvestigationOpen {
			return domain.StateConflict("只能向开放调查添加措施")
		}
		a.ID = s.store.NextID(st, "act")
		st.Actions[a.ID] = a
		lot := st.Lots[inv.LotID]
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lot.ID] = lot
		return nil
	})
	return a, err
}

type UpdateActionInput struct {
	Status         domain.ActionStatus `json:"status"`
	CompletionNote string              `json:"completion_note"`
}

func (s *Service) UpdateAction(actor, id string, in UpdateActionInput) (domain.CAPAAction, error) {
	var result domain.CAPAAction
	lotID, err := s.actionLot(id)
	if err != nil {
		return result, err
	}
	err = s.store.Update(actor, "ACTION_UPDATED", "action", id, lotID, "", string(in.Status), func(st *store.State) error {
		a, ok := st.Actions[id]
		if !ok {
			return domain.NotFound("措施", id)
		}
		if a.Status != domain.ActionOpen {
			return domain.StateConflict("措施已经结束")
		}
		if in.Status != domain.ActionCompleted && in.Status != domain.ActionCancelled {
			return domain.Invalid("status", "措施只能完成或取消")
		}
		if in.Status == domain.ActionCompleted && strings.TrimSpace(in.CompletionNote) == "" {
			return domain.Invalid("completion_note", "完成措施必须填写完成说明")
		}
		a.Status = in.Status
		a.CompletionNote = strings.TrimSpace(in.CompletionNote)
		if in.Status == domain.ActionCompleted {
			now := s.clock.Now()
			a.CompletedAt = &now
		}
		st.Actions[id] = a
		inv := st.Investigations[a.InvestigationID]
		lot := st.Lots[inv.LotID]
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lot.ID] = lot
		result = a
		return nil
	})
	return result, err
}

type SubmitInvestigationInput struct {
	RootCause string `json:"root_cause"`
	Impact    string `json:"impact"`
	Summary   string `json:"summary"`
}

func (s *Service) SubmitInvestigation(actor, id string, in SubmitInvestigationInput) (domain.Investigation, error) {
	var result domain.Investigation
	lotID, err := s.investigationLot(id)
	if err != nil {
		return result, err
	}
	err = s.store.Update(actor, "INVESTIGATION_SUBMITTED", "investigation", id, lotID, "OPEN", "SUBMITTED", func(st *store.State) error {
		inv, ok := st.Investigations[id]
		if !ok {
			return domain.NotFound("调查", id)
		}
		if inv.Status != domain.InvestigationOpen {
			return domain.StateConflict("调查不处于开放状态")
		}
		if strings.TrimSpace(in.RootCause) == "" || strings.TrimSpace(in.Impact) == "" || strings.TrimSpace(in.Summary) == "" {
			return domain.Invalid("conclusion", "根因、影响结论和调查摘要不能为空")
		}
		if len(st.Evidence[id]) == 0 {
			return domain.Invalid("evidence", "提交调查至少需要一条证据")
		}
		active := make(map[string]bool)
		for _, excursion := range st.Excursions[inv.LotID] {
			if excursion.Active {
				active[excursion.ID] = true
			}
		}
		for _, excursionID := range inv.ExcursionIDs {
			if !active[excursionID] {
				return domain.StateConflict("调查覆盖的偏差已撤销，请重新确认调查范围")
			}
		}
		for _, other := range st.Investigations {
			if other.ID == id || other.Status != domain.InvestigationSubmitted {
				continue
			}
			for _, eid := range inv.ExcursionIDs {
				if contains(other.ExcursionIDs, eid) {
					return domain.Conflict("活动偏差已归属其他已提交调查")
				}
			}
		}
		inv.RootCause = strings.TrimSpace(in.RootCause)
		inv.Impact = strings.TrimSpace(in.Impact)
		inv.Summary = strings.TrimSpace(in.Summary)
		inv.Status = domain.InvestigationSubmitted
		now := s.clock.Now()
		inv.SubmittedAt = &now
		st.Investigations[id] = inv
		lot := st.Lots[inv.LotID]
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lot.ID] = lot
		result = inv
		return nil
	})
	return result, err
}

func (s *Service) ReleasePreview(lotID string) (domain.ReleasePreview, error) {
	var out domain.ReleasePreview
	err := s.store.View(func(st store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		out = releasePreview(st, lot, s.clock.Now())
		return nil
	})
	return out, err
}
func releasePreview(st store.State, lot domain.MonitoredLot, now time.Time) domain.ReleasePreview {
	p := domain.ReleasePreview{LotID: lot.ID, Version: lot.Version, Suggested: domain.DecisionRelease, Blockers: []domain.Blocker{}}
	if lot.Status != domain.LotUnderReview {
		p.Blockers = append(p.Blockers, domain.Blocker{Code: "MONITORING_NOT_CLOSED", Message: "批次尚未结束监测"})
	}
	if lot.EvaluationVersion != lot.Version {
		p.Blockers = append(p.Blockers, domain.Blocker{Code: "STALE_EVALUATION", Message: "偏差聚合版本已过期"})
	}
	if len(st.Readings[lot.ID]) == 0 {
		p.Blockers = append(p.Blockers, domain.Blocker{Code: "NO_READINGS", Message: "批次没有可用于放行判断的温度读数"})
	}
	covered := map[string]domain.Investigation{}
	for _, inv := range st.Investigations {
		if inv.LotID != lot.ID {
			continue
		}
		if inv.Status == domain.InvestigationOpen {
			p.Blockers = append(p.Blockers, domain.Blocker{Code: "OPEN_INVESTIGATION", Message: "批次仍有未提交的调查", ObjectID: inv.ID})
		}
		if inv.Status == domain.InvestigationSubmitted {
			for _, id := range inv.ExcursionIDs {
				covered[id] = inv
			}
		}
	}
	for _, e := range st.Excursions[lot.ID] {
		if !e.Active {
			continue
		}
		if e.Type == domain.ExcursionGap {
			p.Blockers = append(p.Blockers, domain.Blocker{Code: "READING_GAP", Message: "存在无法证明温度正常的读数缺口", ObjectID: e.ID})
		}
		if e.Severity == domain.SeverityMajor || e.Severity == domain.SeverityCritical {
			inv, ok := covered[e.ID]
			if !ok {
				p.Blockers = append(p.Blockers, domain.Blocker{Code: "UNINVESTIGATED_EXCURSION", Message: "严重偏差尚未完成调查", ObjectID: e.ID})
				continue
			}
			for _, a := range st.Actions {
				if a.InvestigationID == inv.ID && a.Status == domain.ActionOpen {
					code := "OPEN_ACTION"
					message := "严重偏差仍有未完成措施"
					if a.DueAt.Before(now) {
						code = "OVERDUE_ACTION"
						message = "严重偏差存在逾期措施"
					}
					p.Blockers = append(p.Blockers, domain.Blocker{Code: code, Message: message, ObjectID: a.ID})
				}
			}
		}
	}
	if len(p.Blockers) > 0 {
		p.Suggested = domain.DecisionHold
	}
	return p
}

type CreateDecisionInput struct {
	Decision        domain.DecisionType `json:"decision"`
	Reason          string              `json:"reason"`
	ExpectedVersion int64               `json:"expected_version"`
}

func (s *Service) CreateDecision(actor, lotID string, in CreateDecisionInput) (domain.ReleaseDecision, error) {
	var result domain.ReleaseDecision
	err := s.store.Update(actor, "DECISION_CREATED", "decision", "pending", lotID, "", string(in.Decision), func(st *store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		if lot.Status != domain.LotUnderReview {
			return domain.StateConflict("只有复核中的批次可以提交决定")
		}
		if lot.Version != in.ExpectedVersion {
			return &domain.Error{Code: "VERSION_CONFLICT", Message: "批次版本冲突", Details: map[string]any{"current_version": lot.Version}}
		}
		if in.Decision != domain.DecisionRelease && in.Decision != domain.DecisionHold && in.Decision != domain.DecisionReject {
			return domain.Invalid("decision", "决定类型无效")
		}
		if in.Decision == domain.DecisionReject && strings.TrimSpace(in.Reason) == "" {
			return domain.Invalid("reason", "拒绝必须填写理由")
		}
		preview := releasePreview(*st, lot, s.clock.Now())
		if in.Decision == domain.DecisionRelease && len(preview.Blockers) > 0 {
			return domain.Conflict("批次存在放行阻断项")
		}
		result = domain.ReleaseDecision{ID: s.store.NextID(st, "dec"), LotID: lotID, Decision: in.Decision, Reason: strings.TrimSpace(in.Reason), ExpectedVersion: in.ExpectedVersion, Blockers: preview.Blockers, Actor: actor, CreatedAt: s.clock.Now()}
		st.Decisions[lotID] = append(st.Decisions[lotID], result)
		if in.Decision == domain.DecisionRelease {
			lot.Status = domain.LotReleased
		} else if in.Decision == domain.DecisionReject {
			lot.Status = domain.LotRejected
		}
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lotID] = lot
		return nil
	})
	return result, err
}

func (s *Service) Audit(lotID string, cursor int64, limit int) ([]domain.AuditEvent, int64, bool, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		return nil, 0, false, domain.Invalid("limit", "分页大小不能超过 500")
	}
	var out []domain.AuditEvent
	next := int64(0)
	valid := false
	err := s.store.View(func(st store.State) error {
		if _, ok := st.Lots[lotID]; !ok {
			return domain.NotFound("批次", lotID)
		}
		valid = store.VerifyAudit(st.Audit) == nil
		for _, e := range st.Audit {
			if e.LotID == lotID && e.Sequence > cursor {
				out = append(out, e)
			}
		}
		if len(out) > limit {
			next = out[limit-1].Sequence
			out = out[:limit]
		}
		return nil
	})
	return out, next, valid, err
}

type CaseExport struct {
	SchemaVersion  int                         `json:"schema_version"`
	ExportedAt     time.Time                   `json:"exported_at"`
	Lot            domain.MonitoredLot         `json:"lot"`
	Policy         domain.TemperaturePolicy    `json:"policy"`
	Readings       []domain.TemperatureReading `json:"readings"`
	Excursions     []domain.Excursion          `json:"excursions"`
	Investigations []domain.Investigation      `json:"investigations"`
	Evidence       []domain.Evidence           `json:"evidence"`
	Actions        []domain.CAPAAction         `json:"actions"`
	Decisions      []domain.ReleaseDecision    `json:"decisions"`
	Audit          []domain.AuditEvent         `json:"audit"`
	AuditValid     bool                        `json:"audit_valid"`
	Compliance     ComplianceSummary           `json:"compliance"`
}

func (s *Service) Export(lotID string) (CaseExport, error) {
	var out CaseExport
	err := s.store.View(func(st store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		out = CaseExport{SchemaVersion: domain.SchemaVersion, ExportedAt: s.clock.Now(), Lot: lot, Policy: st.Policies[lot.PolicyID], Readings: st.Readings[lotID], Excursions: st.Excursions[lotID], Decisions: st.Decisions[lotID], AuditValid: store.VerifyAudit(st.Audit) == nil}
		out.Compliance = buildComplianceSummary(st, lot, s.clock.Now())
		for _, inv := range st.Investigations {
			if inv.LotID == lotID {
				out.Investigations = append(out.Investigations, inv)
				out.Evidence = append(out.Evidence, st.Evidence[inv.ID]...)
				for _, a := range st.Actions {
					if a.InvestigationID == inv.ID {
						out.Actions = append(out.Actions, a)
					}
				}
			}
		}
		for _, e := range st.Audit {
			if e.LotID == lotID {
				out.Audit = append(out.Audit, e)
			}
		}
		sort.Slice(out.Investigations, func(i, j int) bool { return out.Investigations[i].ID < out.Investigations[j].ID })
		sort.Slice(out.Actions, func(i, j int) bool { return out.Actions[i].ID < out.Actions[j].ID })
		return nil
	})
	return out, err
}

func (s *Service) ScanOverdue(actor string) (int, error) {
	count := 0
	due := false
	if err := s.store.View(func(st store.State) error {
		for _, a := range st.Actions {
			if a.Status == domain.ActionOpen && !a.OverdueNotified && a.DueAt.Before(s.clock.Now()) {
				due = true
				break
			}
		}
		return nil
	}); err != nil || !due {
		return 0, err
	}
	err := s.store.Update(actor, "OVERDUE_SCAN", "system", "overdue-scanner", "", "", s.clock.Now().Format(time.RFC3339), func(st *store.State) error {
		for id, a := range st.Actions {
			if a.Status == domain.ActionOpen && !a.OverdueNotified && a.DueAt.Before(s.clock.Now()) {
				a.OverdueNotified = true
				st.Actions[id] = a
				inv := st.Investigations[a.InvestigationID]
				lot := st.Lots[inv.LotID]
				lot.Version++
				lot.EvaluationVersion = lot.Version
				st.Lots[lot.ID] = lot
				count++
			}
		}
		return nil
	})
	return count, err
}

// PendingEvaluationLotIDs 为后台补偿扫描提供稳定、有界的待办列表。
func (s *Service) PendingEvaluationLotIDs() ([]string, error) {
	result := make([]string, 0)
	err := s.store.View(func(st store.State) error {
		for _, lot := range st.Lots {
			if (lot.Status == domain.LotMonitoring || lot.Status == domain.LotUnderReview) && lot.EvaluationVersion != lot.Version {
				result = append(result, lot.ID)
			}
		}
		sort.Strings(result)
		return nil
	})
	return result, err
}

func digestJSON(values ...any) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (s *Service) investigationLot(id string) (string, error) {
	var lotID string
	err := s.store.View(func(st store.State) error {
		inv, ok := st.Investigations[id]
		if !ok {
			return domain.NotFound("调查", id)
		}
		lotID = inv.LotID
		return nil
	})
	return lotID, err
}

func (s *Service) actionLot(id string) (string, error) {
	var lotID string
	err := s.store.View(func(st store.State) error {
		action, ok := st.Actions[id]
		if !ok {
			return domain.NotFound("措施", id)
		}
		inv, ok := st.Investigations[action.InvestigationID]
		if !ok {
			return domain.NotFound("调查", action.InvestigationID)
		}
		lotID = inv.LotID
		return nil
	})
	return lotID, err
}
