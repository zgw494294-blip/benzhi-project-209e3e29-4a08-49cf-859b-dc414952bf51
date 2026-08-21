package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"thermoguard/internal/app"
	"thermoguard/internal/clock"
	"thermoguard/internal/domain"
	"thermoguard/internal/httpapi"
	"thermoguard/internal/store"
)

type testServer struct {
	baseURL  string
	server   *http.Server
	listener net.Listener
}

func startServer(t *testing.T, service *app.Service) *testServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: httpapi.New(service, func() bool { return true }).Handler(), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	return &testServer{baseURL: "http://" + listener.Addr().String(), server: server, listener: listener}
}
func (s *testServer) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type responseEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta json.RawMessage `json:"meta"`
}

func call(t *testing.T, server *testServer, method, path string, body any, status int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, server.baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" {
		req.Header.Set("X-Actor", "acceptance-user")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s 状态=%d，响应=%s", method, path, resp.StatusCode, data)
	}
	if out != nil {
		var env responseEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("解析响应: %v，原文=%s", err, data)
		}
	}
}

func createPublishedPolicy(t *testing.T, server *testServer) domain.TemperaturePolicy {
	t.Helper()
	var policy domain.TemperaturePolicy
	call(t, server, "POST", "/api/v1/policies", map[string]any{"name": "2-8 摄氏度验证规则", "min_c": 2, "max_c": 8, "max_gap_minutes": 30, "continuous_minutes": 10, "cumulative_minutes": 10, "major_delta_c": 2, "critical_delta_c": 4}, http.StatusCreated, &policy)
	call(t, server, "POST", "/api/v1/policies/"+policy.ID+"/publish", nil, http.StatusOK, &policy)
	return policy
}
func createLot(t *testing.T, server *testServer, policyID, code string, base time.Time) domain.MonitoredLot {
	t.Helper()
	var lot domain.MonitoredLot
	call(t, server, "POST", "/api/v1/lots", map[string]any{"code": code, "product": "冻存生物样本", "policy_id": policyID, "start_at": base, "end_at": base.Add(2 * time.Hour)}, http.StatusCreated, &lot)
	return lot
}

func TestEndToEndExcursionRelease(t *testing.T) {
	dir := t.TempDir()
	repo, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 2, 0, 0, 0, time.UTC)
	service := app.New(repo, clock.NewFixed(now))
	server := startServer(t, service)
	defer server.close(t)
	policy := createPublishedPolicy(t, server)
	lot := createLot(t, server, policy.ID, "LOT-E2E-001", now)
	var readingResults []app.ReadingResult
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/readings:batch", map[string]any{"readings": []map[string]any{{"device_id": "probe-A", "sampled_at": now, "celsius": 12, "source": "logger", "idempotency_key": "r-1"}, {"device_id": "probe-A", "sampled_at": now.Add(15 * time.Minute), "celsius": 13, "source": "logger", "idempotency_key": "r-2"}}}, http.StatusCreated, &readingResults)
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/monitoring:close", nil, http.StatusOK, &lot)
	var excursions []domain.Excursion
	call(t, server, "GET", "/api/v1/lots/"+lot.ID+"/excursions", nil, http.StatusOK, &excursions)
	if len(excursions) < 1 {
		t.Fatal("未产生温控偏差")
	}
	ids := make([]string, len(excursions))
	for i, e := range excursions {
		ids[i] = e.ID
	}
	var inv domain.Investigation
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/investigations", map[string]any{"excursion_ids": ids}, http.StatusCreated, &inv)
	var evidence domain.Evidence
	call(t, server, "POST", "/api/v1/investigations/"+inv.ID+"/evidence", map[string]any{"category": "LOGGER_CHECK", "reference": "校准证书-2026", "collector": "质量专员", "summary": "记录仪校准有效，确认发生真实高温暴露", "collected_at": now}, http.StatusCreated, &evidence)
	var action domain.CAPAAction
	call(t, server, "POST", "/api/v1/investigations/"+inv.ID+"/actions", map[string]any{"type": "CORRECTIVE", "description": "更换保温箱并复核包装验证", "owner": "冷链主管", "due_at": now.Add(24 * time.Hour)}, http.StatusCreated, &action)
	call(t, server, "POST", "/api/v1/investigations/"+inv.ID+"/submit", map[string]any{"root_cause": "保温箱密封条失效", "impact": "稳定性资料支持本次暴露，无质量影响", "summary": "证据与稳定性边界已复核"}, http.StatusOK, &inv)
	var preview domain.ReleasePreview
	call(t, server, "GET", "/api/v1/lots/"+lot.ID+"/release-preview", nil, http.StatusOK, &preview)
	if len(preview.Blockers) == 0 {
		t.Fatal("未完成措施应阻断放行")
	}
	call(t, server, "PATCH", "/api/v1/actions/"+action.ID, map[string]any{"status": "COMPLETED", "completion_note": "新保温箱验证合格并投入使用"}, http.StatusOK, &action)
	call(t, server, "GET", "/api/v1/lots/"+lot.ID+"/release-preview", nil, http.StatusOK, &preview)
	if len(preview.Blockers) != 0 {
		t.Fatalf("完成闭环后仍有阻断: %#v", preview.Blockers)
	}
	var decision domain.ReleaseDecision
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/decisions", map[string]any{"decision": "RELEASE", "reason": "调查与措施均完成", "expected_version": preview.Version}, http.StatusCreated, &decision)
	var exported app.CaseExport
	call(t, server, "GET", "/api/v1/lots/"+lot.ID+"/case-export", nil, http.StatusOK, &exported)
	if exported.Lot.Status != domain.LotReleased || !exported.AuditValid || len(exported.Audit) < 8 {
		t.Fatalf("案件包不完整: status=%s audit=%d valid=%v", exported.Lot.Status, len(exported.Audit), exported.AuditValid)
	}
}

func TestRestartRecoveryAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	repo, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := startServer(t, app.New(repo, clock.NewFixed(now)))
	policy := createPublishedPolicy(t, server)
	lot := createLot(t, server, policy.ID, "LOT-RESTART-001", now)
	reading := map[string]any{"device_id": "probe-B", "sampled_at": now.Add(20 * time.Minute), "celsius": 12, "source": "import", "idempotency_key": "stable-key"}
	var first app.ReadingResult
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/readings", reading, http.StatusCreated, &first)
	var before struct {
		Lot domain.MonitoredLot `json:"lot"`
	}
	call(t, server, "GET", "/api/v1/lots/"+lot.ID, nil, http.StatusOK, &before)
	server.close(t)
	reopened, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	server = startServer(t, app.New(reopened, clock.NewFixed(now)))
	defer server.close(t)
	var duplicate app.ReadingResult
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/readings", reading, http.StatusCreated, &duplicate)
	if !duplicate.Duplicate || duplicate.Reading.ID != first.Reading.ID {
		t.Fatalf("重启后幂等结果不一致: %#v", duplicate)
	}
	var afterDuplicate struct {
		Lot domain.MonitoredLot `json:"lot"`
	}
	call(t, server, "GET", "/api/v1/lots/"+lot.ID, nil, http.StatusOK, &afterDuplicate)
	if afterDuplicate.Lot.Version != before.Lot.Version {
		t.Fatalf("纯重复请求推进了批次版本: %d -> %d", before.Lot.Version, afterDuplicate.Lot.Version)
	}
	second := map[string]any{"device_id": "probe-B", "sampled_at": now, "celsius": 11, "source": "import", "idempotency_key": "out-of-order"}
	var added app.ReadingResult
	call(t, server, "POST", "/api/v1/lots/"+lot.ID+"/readings", second, http.StatusCreated, &added)
	var exportOne app.CaseExport
	call(t, server, "GET", "/api/v1/lots/"+lot.ID+"/case-export", nil, http.StatusOK, &exportOne)
	if !exportOne.AuditValid || len(exportOne.Readings) != 2 {
		t.Fatalf("首次恢复数据不完整: %#v", exportOne)
	}
	fingerprints := map[string]bool{}
	for _, e := range exportOne.Excursions {
		fingerprints[e.Fingerprint] = true
	}
	server.close(t)
	finalRepo, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	finalService := app.New(finalRepo, clock.NewFixed(now))
	exportTwo, err := finalService.Export(lot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exportTwo.Readings) != 2 || len(exportTwo.Audit) != len(exportOne.Audit) {
		t.Fatalf("再次恢复不一致: readings=%d audit=%d", len(exportTwo.Readings), len(exportTwo.Audit))
	}
	for _, e := range exportTwo.Excursions {
		if !fingerprints[e.Fingerprint] {
			t.Fatalf("偏差指纹在重启后改变: %s", e.Fingerprint)
		}
	}
	if !finalRepo.Health().AuditValid {
		t.Fatal("重启后审计链无效")
	}
}
