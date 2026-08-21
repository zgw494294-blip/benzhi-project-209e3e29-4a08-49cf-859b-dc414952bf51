package app

import (
	"testing"
	"time"

	"thermoguard/internal/clock"
	"thermoguard/internal/domain"
	"thermoguard/internal/store"
)

func newTestService(t *testing.T, now time.Time) *Service {
	t.Helper()
	repo, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(repo, clock.NewFixed(now))
}

func publishedPolicyAndLot(t *testing.T, service *Service, now time.Time) (domain.TemperaturePolicy, domain.MonitoredLot) {
	t.Helper()
	policy, err := service.CreatePolicy("tester", CreatePolicyInput{
		Name: "测试规则", MinC: 2, MaxC: 8, MaxGapMinutes: 30,
		ContinuousMinutes: 10, CumulativeMinutes: 20, MajorDeltaC: 2, CriticalDeltaC: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err = service.PublishPolicy("tester", policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	lot, err := service.CreateLot("tester", CreateLotInput{
		Code: "LOT-TEST", Product: "测试样本", PolicyID: policy.ID,
		StartAt: now, EndAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy, lot
}

func TestReleaseRequiresReadingsAndHoldKeepsEvaluationCurrent(t *testing.T) {
	now := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	service := newTestService(t, now)
	_, lot := publishedPolicyAndLot(t, service, now)
	closed, err := service.CloseMonitoring("tester", lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.ReleasePreview(lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(preview.Blockers, "NO_READINGS") {
		t.Fatalf("无读数批次缺少放行阻断: %#v", preview.Blockers)
	}
	decision, err := service.CreateDecision("tester", lot.ID, CreateDecisionInput{
		Decision: domain.DecisionHold, Reason: "等待记录仪数据", ExpectedVersion: closed.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != domain.DecisionHold {
		t.Fatalf("暂缓决定错误: %#v", decision)
	}
	preview, err = service.ReleasePreview(lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasBlocker(preview.Blockers, "STALE_EVALUATION") {
		t.Fatalf("暂缓决定不应使温控评估过期: %#v", preview.Blockers)
	}
}

func TestReadingRequiresUTCAndSource(t *testing.T) {
	now := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	service := newTestService(t, now)
	policy, lot := publishedPolicyAndLot(t, service, now)
	_, err := service.CreateLot("tester", CreateLotInput{
		Code: "LOT-OFFSET", Product: "测试样本", PolicyID: policy.ID,
		StartAt: time.Date(2026, 8, 21, 11, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
		EndAt:   time.Date(2026, 8, 21, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
	})
	if err == nil {
		t.Fatal("非 UTC 批次时间窗未被拒绝")
	}
	_, err = service.AddReading("tester", lot.ID, AddReadingInput{
		DeviceID: "probe", SampledAt: now, Celsius: 5, IdempotencyKey: "missing-source",
	})
	if err == nil {
		t.Fatal("空读数来源未被拒绝")
	}
}

func TestOpenInvestigationBlocksRelease(t *testing.T) {
	now := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	service := newTestService(t, now)
	_, lot := publishedPolicyAndLot(t, service, now)
	if _, err := service.AddReadings("tester", lot.ID, []AddReadingInput{
		{DeviceID: "probe", SampledAt: now, Celsius: 12, Source: "logger", IdempotencyKey: "one"},
		{DeviceID: "probe", SampledAt: now.Add(15 * time.Minute), Celsius: 12, Source: "logger", IdempotencyKey: "two"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloseMonitoring("tester", lot.ID); err != nil {
		t.Fatal(err)
	}
	excursions, err := service.ListExcursions(lot.ID, false)
	if err != nil || len(excursions) == 0 {
		t.Fatalf("缺少测试偏差: excursions=%#v err=%v", excursions, err)
	}
	inv, err := service.CreateInvestigation("tester", lot.ID, CreateInvestigationInput{ExcursionIDs: []string{excursions[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.ReleasePreview(lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(preview.Blockers, "OPEN_INVESTIGATION") {
		t.Fatalf("开放调查 %s 未阻断放行: %#v", inv.ID, preview.Blockers)
	}
}

func TestListValidation(t *testing.T) {
	now := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	service := newTestService(t, now)
	_, lot := publishedPolicyAndLot(t, service, now)
	if _, _, err := service.ListLots(domain.LotStatus("UNKNOWN"), "", "", 10); err == nil {
		t.Fatal("未知批次状态未被拒绝")
	}
	from, to := now.Add(time.Hour), now
	if _, err := service.ListReadings(lot.ID, &from, &to); err == nil {
		t.Fatal("反向时间范围未被拒绝")
	}
}

func hasBlocker(blockers []domain.Blocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}
