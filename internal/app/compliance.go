package app

import (
	"sort"
	"time"

	"thermoguard/internal/domain"
	"thermoguard/internal/store"
)

type SeverityCount struct {
	Minor    int `json:"minor"`
	Major    int `json:"major"`
	Critical int `json:"critical"`
}

type ComplianceSummary struct {
	LotID                   string                  `json:"lot_id"`
	LotStatus               domain.LotStatus        `json:"lot_status"`
	ReadingCount            int                     `json:"reading_count"`
	ActiveExcursionCount    int                     `json:"active_excursion_count"`
	RevokedExcursionCount   int                     `json:"revoked_excursion_count"`
	ExcursionsBySeverity    SeverityCount           `json:"excursions_by_severity"`
	OpenInvestigations      int                     `json:"open_investigations"`
	SubmittedInvestigations int                     `json:"submitted_investigations"`
	VoidInvestigations      int                     `json:"void_investigations"`
	OpenActions             int                     `json:"open_actions"`
	CompletedActions        int                     `json:"completed_actions"`
	CancelledActions        int                     `json:"cancelled_actions"`
	OverdueActions          int                     `json:"overdue_actions"`
	UncoveredExcursionIDs   []string                `json:"uncovered_excursion_ids"`
	OpenActionIDs           []string                `json:"open_action_ids"`
	LatestDecision          *domain.ReleaseDecision `json:"latest_decision,omitempty"`
	ReleasePreview          domain.ReleasePreview   `json:"release_preview"`
	AuditEventCount         int                     `json:"audit_event_count"`
	AuditChainValid         bool                    `json:"audit_chain_valid"`
	GeneratedAt             time.Time               `json:"generated_at"`
}

func (s *Service) ComplianceSummary(lotID string) (ComplianceSummary, error) {
	var result ComplianceSummary
	err := s.store.View(func(st store.State) error {
		lot, ok := st.Lots[lotID]
		if !ok {
			return domain.NotFound("批次", lotID)
		}
		result = buildComplianceSummary(st, lot, s.clock.Now())
		return nil
	})
	return result, err
}

func buildComplianceSummary(st store.State, lot domain.MonitoredLot, now time.Time) ComplianceSummary {
	result := ComplianceSummary{
		LotID: lot.ID, LotStatus: lot.Status, ReadingCount: len(st.Readings[lot.ID]),
		ReleasePreview: releasePreview(st, lot, now), AuditChainValid: store.VerifyAudit(st.Audit) == nil,
		GeneratedAt: now,
	}
	covered := make(map[string]bool)
	lotInvestigations := make(map[string]bool)
	for _, investigation := range st.Investigations {
		if investigation.LotID != lot.ID {
			continue
		}
		lotInvestigations[investigation.ID] = true
		switch investigation.Status {
		case domain.InvestigationOpen:
			result.OpenInvestigations++
		case domain.InvestigationSubmitted:
			result.SubmittedInvestigations++
			for _, excursionID := range investigation.ExcursionIDs {
				covered[excursionID] = true
			}
		case domain.InvestigationVoid:
			result.VoidInvestigations++
		}
	}
	for _, excursion := range st.Excursions[lot.ID] {
		if !excursion.Active {
			result.RevokedExcursionCount++
			continue
		}
		result.ActiveExcursionCount++
		switch excursion.Severity {
		case domain.SeverityMinor:
			result.ExcursionsBySeverity.Minor++
		case domain.SeverityMajor:
			result.ExcursionsBySeverity.Major++
		case domain.SeverityCritical:
			result.ExcursionsBySeverity.Critical++
		}
		if !covered[excursion.ID] {
			result.UncoveredExcursionIDs = append(result.UncoveredExcursionIDs, excursion.ID)
		}
	}
	for _, action := range st.Actions {
		if !lotInvestigations[action.InvestigationID] {
			continue
		}
		switch action.Status {
		case domain.ActionOpen:
			result.OpenActions++
			result.OpenActionIDs = append(result.OpenActionIDs, action.ID)
			if action.DueAt.Before(now) {
				result.OverdueActions++
			}
		case domain.ActionCompleted:
			result.CompletedActions++
		case domain.ActionCancelled:
			result.CancelledActions++
		}
	}
	decisions := st.Decisions[lot.ID]
	if len(decisions) > 0 {
		latest := decisions[len(decisions)-1]
		result.LatestDecision = &latest
	}
	for _, event := range st.Audit {
		if event.LotID == lot.ID {
			result.AuditEventCount++
		}
	}
	sort.Strings(result.UncoveredExcursionIDs)
	sort.Strings(result.OpenActionIDs)
	return result
}
