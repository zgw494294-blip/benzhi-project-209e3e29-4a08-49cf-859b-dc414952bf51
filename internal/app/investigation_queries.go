package app

import (
	"sort"
	"time"

	"thermoguard/internal/domain"
	"thermoguard/internal/store"
)

type ActionView struct {
	domain.CAPAAction
	Overdue bool `json:"overdue"`
}

type InvestigationCase struct {
	Investigation domain.Investigation `json:"investigation"`
	Excursions    []domain.Excursion   `json:"excursions"`
	Evidence      []domain.Evidence    `json:"evidence"`
	Actions       []ActionView         `json:"actions"`
	Complete      bool                 `json:"complete"`
	Missing       []string             `json:"missing"`
}

func (s *Service) GetInvestigation(id string) (InvestigationCase, error) {
	var result InvestigationCase
	err := s.store.View(func(st store.State) error {
		inv, ok := st.Investigations[id]
		if !ok {
			return domain.NotFound("调查", id)
		}
		result = buildInvestigationCase(st, inv, s.clock.Now())
		return nil
	})
	return result, err
}

func (s *Service) ListInvestigations(lotID string, status domain.InvestigationStatus) ([]InvestigationCase, error) {
	result := make([]InvestigationCase, 0)
	err := s.store.View(func(st store.State) error {
		if _, ok := st.Lots[lotID]; !ok {
			return domain.NotFound("批次", lotID)
		}
		for _, inv := range st.Investigations {
			if inv.LotID != lotID || status != "" && inv.Status != status {
				continue
			}
			result = append(result, buildInvestigationCase(st, inv, s.clock.Now()))
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].Investigation.ID < result[j].Investigation.ID
		})
		return nil
	})
	return result, err
}

func buildInvestigationCase(st store.State, inv domain.Investigation, now time.Time) InvestigationCase {
	result := InvestigationCase{Investigation: inv, Evidence: append([]domain.Evidence(nil), st.Evidence[inv.ID]...)}
	wanted := make(map[string]bool, len(inv.ExcursionIDs))
	for _, id := range inv.ExcursionIDs {
		wanted[id] = true
	}
	for _, excursion := range st.Excursions[inv.LotID] {
		if wanted[excursion.ID] {
			result.Excursions = append(result.Excursions, excursion)
		}
	}
	for _, action := range st.Actions {
		if action.InvestigationID == inv.ID {
			result.Actions = append(result.Actions, ActionView{CAPAAction: action, Overdue: action.Status == domain.ActionOpen && action.DueAt.Before(now)})
		}
	}
	sort.Slice(result.Excursions, func(i, j int) bool { return result.Excursions[i].ID < result.Excursions[j].ID })
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].ID < result.Evidence[j].ID })
	sort.Slice(result.Actions, func(i, j int) bool { return result.Actions[i].ID < result.Actions[j].ID })
	if len(result.Evidence) == 0 {
		result.Missing = append(result.Missing, "EVIDENCE")
	}
	if inv.RootCause == "" {
		result.Missing = append(result.Missing, "ROOT_CAUSE")
	}
	if inv.Impact == "" {
		result.Missing = append(result.Missing, "IMPACT")
	}
	if inv.Summary == "" {
		result.Missing = append(result.Missing, "SUMMARY")
	}
	result.Complete = inv.Status == domain.InvestigationSubmitted && len(result.Missing) == 0
	return result
}

func (s *Service) VoidInvestigation(actor, id, reason string) (domain.Investigation, error) {
	var result domain.Investigation
	if reason == "" {
		return result, domain.Invalid("reason", "作废调查必须填写理由")
	}
	lotID, err := s.investigationLot(id)
	if err != nil {
		return result, err
	}
	err = s.store.Update(actor, "INVESTIGATION_VOIDED", "investigation", id, lotID, "OPEN", "VOID", func(st *store.State) error {
		inv, ok := st.Investigations[id]
		if !ok {
			return domain.NotFound("调查", id)
		}
		if inv.Status != domain.InvestigationOpen {
			return domain.StateConflict("只能作废开放调查")
		}
		lot := st.Lots[inv.LotID]
		if lot.Status == domain.LotReleased || lot.Status == domain.LotRejected {
			return domain.StateConflict("终态批次引用的调查不能作废")
		}
		inv.Status = domain.InvestigationVoid
		inv.Summary = "作废原因：" + reason
		st.Investigations[id] = inv
		lot.Version++
		lot.EvaluationVersion = lot.Version
		st.Lots[lot.ID] = lot
		result = inv
		return nil
	})
	return result, err
}

func (s *Service) ListActions(lotID string, status domain.ActionStatus) ([]ActionView, error) {
	result := make([]ActionView, 0)
	err := s.store.View(func(st store.State) error {
		if _, ok := st.Lots[lotID]; !ok {
			return domain.NotFound("批次", lotID)
		}
		investigations := make(map[string]bool)
		for _, inv := range st.Investigations {
			if inv.LotID == lotID {
				investigations[inv.ID] = true
			}
		}
		for _, action := range st.Actions {
			if !investigations[action.InvestigationID] || status != "" && action.Status != status {
				continue
			}
			result = append(result, ActionView{CAPAAction: action, Overdue: action.Status == domain.ActionOpen && action.DueAt.Before(s.clock.Now())})
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].DueAt.Equal(result[j].DueAt) {
				return result[i].ID < result[j].ID
			}
			return result[i].DueAt.Before(result[j].DueAt)
		})
		return nil
	})
	return result, err
}
