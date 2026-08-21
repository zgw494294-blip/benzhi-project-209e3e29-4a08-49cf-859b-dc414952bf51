package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"thermoguard/internal/app"
	"thermoguard/internal/domain"
)

func TestServiceProcessSmoke(t *testing.T) {
	address := freeHighLoopbackAddress(t)
	binary := filepath.Join(t.TempDir(), "thermoguard-smoke")
	build := exec.Command("go", "build", "-o", binary, "./cmd/thermoguard")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("编译冒烟二进制失败: %v\n%s", err, output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	command := exec.CommandContext(ctx, binary, "-addr", address, "-data-dir", t.TempDir())
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cancel()
			<-done
		}
	}()
	baseURL := "http://" + address
	deadline := time.Now().Add(8 * time.Second)
	for {
		response, err := http.Get(baseURL + "/api/v1/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("服务未按时就绪: %s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	shim := &testServer{baseURL: baseURL}
	policy := createPublishedPolicy(t, shim)
	now := time.Now().UTC().Truncate(time.Second)
	lot := createLot(t, shim, policy.ID, "LOT-PROCESS-SMOKE", now)
	var reading app.ReadingResult
	call(t, shim, "POST", "/api/v1/lots/"+lot.ID+"/readings", map[string]any{
		"device_id": "smoke-probe", "sampled_at": now, "celsius": 5.0,
		"source": "process-smoke", "idempotency_key": "smoke-reading-1",
	}, http.StatusCreated, &reading)
	var preview domain.ReleasePreview
	call(t, shim, "GET", "/api/v1/lots/"+lot.ID+"/release-preview", nil, http.StatusOK, &preview)
	if preview.LotID != lot.ID {
		t.Fatalf("放行预览批次错误: %s", preview.LotID)
	}
}

func freeHighLoopbackAddress(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if port != 3000 && port != 8080 && port >= 1024 {
			return fmt.Sprintf("127.0.0.1:%d", port)
		}
	}
	t.Fatal("无法分配高位回环端口")
	return ""
}
