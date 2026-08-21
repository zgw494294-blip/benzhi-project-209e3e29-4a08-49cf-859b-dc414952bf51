package importer

import (
	"strings"
	"testing"
)

func TestParseNDJSONStrictly(t *testing.T) {
	valid := "{\"device_id\":\"probe\",\"sampled_at\":\"2026-08-21T03:00:00Z\",\"celsius\":5,\"source\":\"logger\",\"idempotency_key\":\"one\"}\n"
	items, err := ParseNDJSON(strings.NewReader(valid), ParseOptions{})
	if err != nil || len(items) != 1 {
		t.Fatalf("有效文件解析失败: items=%d err=%v", len(items), err)
	}
	unknown := strings.Replace(valid, "\"source\"", "\"unexpected\"", 1)
	if _, err := ParseNDJSON(strings.NewReader(unknown), ParseOptions{}); err == nil {
		t.Fatal("未知字段未被拒绝")
	}
	nonUTC := strings.Replace(valid, "2026-08-21T03:00:00Z", "2026-08-21T11:00:00+08:00", 1)
	if _, err := ParseNDJSON(strings.NewReader(nonUTC), ParseOptions{}); err == nil {
		t.Fatal("非 UTC 时间未被拒绝")
	}
}
