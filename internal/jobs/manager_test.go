package jobs

import (
	"context"
	"testing"
	"time"

	"thermoguard/internal/app"
	"thermoguard/internal/clock"
	"thermoguard/internal/store"
)

func TestManagerReportsEvaluationFailureAndDrains(t *testing.T) {
	repo, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := New(app.New(repo, clock.Real{}), 2, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	if !manager.Enqueue("missing-lot") {
		t.Fatal("首次评估任务不应被拒绝")
	}
	deadline := time.Now().Add(time.Second)
	for manager.LastError() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.LastError() == "" || manager.Healthy() {
		t.Fatalf("失败任务未反映到健康状态: healthy=%v error=%q", manager.Healthy(), manager.LastError())
	}
	cancel()
	manager.Wait()
	if manager.Healthy() {
		t.Fatal("停止后的任务管理器不应健康")
	}
}

func TestPeriodicScanRepairsMissedEvaluation(t *testing.T) {
	now := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	repo, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := app.New(repo, clock.NewFixed(now))
	policy, err := service.CreatePolicy("tester", app.CreatePolicyInput{Name: "扫描规则", MinC: 2, MaxC: 8, MaxGapMinutes: 30, ContinuousMinutes: 10, CumulativeMinutes: 20, MajorDeltaC: 2, CriticalDeltaC: 5})
	if err != nil {
		t.Fatal(err)
	}
	policy, err = service.PublishPolicy("tester", policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	lot, err := service.CreateLot("tester", app.CreateLotInput{Code: "LOT-SCAN", Product: "测试样本", PolicyID: policy.ID, StartAt: now, EndAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Update("tester", "SIMULATE_MISSED_QUEUE", "lot", lot.ID, lot.ID, "", "", func(st *store.State) error {
		changed := st.Lots[lot.ID]
		changed.Version++
		st.Lots[lot.ID] = changed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := New(service, 2, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, _, getErr := service.GetLot(lot.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.EvaluationVersion == current.Version {
			cancel()
			manager.Wait()
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	manager.Wait()
	t.Fatal("周期补偿扫描未修复落后的评估版本")
}
