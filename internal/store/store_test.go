package store

import (
	"os"
	"path/filepath"
	"testing"

	"thermoguard/internal/domain"
)

func TestRecoveryAndTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Update("tester", "CHANGE", "test", "one", "", "", "ok", func(st *State) error { st.NextID++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"schema_version":1,"revision":0}`), 0o640); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.ndjson"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"revision":2`)
	_ = f.Close()
	recovered, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := recovered.Health()
	if h.Revision != 1 || !h.JournalTailTruncated || !h.AuditValid {
		t.Fatalf("恢复健康状态错误: %#v", h)
	}
	if err := recovered.Update("tester", "CHANGE", "test", "two", "", "", "ok", func(st *State) error { st.NextID++; return nil }); err != nil {
		t.Fatalf("修复截断尾部后无法继续写入: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("截断恢复后再次打开失败: %v", err)
	}
	if reopened.Health().Revision != 2 || !reopened.Health().AuditValid {
		t.Fatalf("截断恢复后状态错误: %#v", reopened.Health())
	}
}

func TestAuditTamperingRejected(t *testing.T) {
	events := []domain.AuditEvent{{Sequence: 1, Actor: "a", Action: "x"}}
	events[0].Hash = AuditHash(events[0])
	events[0].Action = "tampered"
	if VerifyAudit(events) == nil {
		t.Fatal("篡改审计事件未被拒绝")
	}
}
