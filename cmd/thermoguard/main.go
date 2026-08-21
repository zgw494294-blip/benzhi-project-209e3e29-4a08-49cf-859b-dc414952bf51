package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"thermoguard/internal/app"
	"thermoguard/internal/clock"
	"thermoguard/internal/httpapi"
	"thermoguard/internal/jobs"
	runtimecfg "thermoguard/internal/runtime"
	"thermoguard/internal/store"
)

func main() {
	if err := run(os.Args[1:], os.Getenv("PORT")); err != nil {
		log.Fatal(err)
	}
}
func run(args []string, portEnv string) error {
	fs := flag.NewFlagSet("thermoguard", flag.ContinueOnError)
	address := fs.String("addr", runtimecfg.DefaultAddress, "监听地址")
	dataDir := fs.String("data-dir", "./var/data", "数据目录")
	selfCheck := fs.Bool("self-check", false, "执行启动自检后退出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			explicit = true
		}
	})
	resolved, err := runtimecfg.ResolveAddress(*address, explicit, portEnv)
	if err != nil {
		return err
	}
	repo, err := store.Open(*dataDir)
	if err != nil {
		return fmt.Errorf("打开仓储: %w", err)
	}
	service := app.New(repo, clock.Real{})
	if *selfCheck {
		return performSelfCheck(resolved, *dataDir, service)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("tcp", resolved)
	if err != nil {
		return err
	}
	jobCtx, stopJobs := context.WithCancel(context.Background())
	manager := jobs.New(service, 256, time.Minute)
	service.SetEvaluateEnqueuer(manager.Enqueue)
	manager.Start(jobCtx)
	api := httpapi.New(service, manager.Healthy, manager.LastError)
	server := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	errCh := make(chan error, 1)
	go func() { log.Printf("衡温冷链服务已监听 %s", listener.Addr()); errCh <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := server.Shutdown(shutdownCtx); err != nil {
			stopJobs()
			manager.Wait()
			return err
		}
		stopJobs()
		manager.Wait()
		return nil
	case err := <-errCh:
		stopJobs()
		manager.Wait()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func performSelfCheck(address, dataDir string, service *app.Service) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("监听配置自检失败: %w", err)
	}
	_ = listener.Close()
	p, err := service.CreatePolicy("self-check", app.CreatePolicyInput{Name: "自检规则", MinC: 2, MaxC: 8, MaxGapMinutes: 30, ContinuousMinutes: 10, CumulativeMinutes: 20, MajorDeltaC: 2, CriticalDeltaC: 5})
	if err != nil {
		return err
	}
	p, err = service.PublishPolicy("self-check", p.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	lot, err := service.CreateLot("self-check", app.CreateLotInput{Code: "SELF-CHECK-" + now.Format("20060102150405"), Product: "自检样本", PolicyID: p.ID, StartAt: now, EndAt: now.Add(time.Hour)})
	if err != nil {
		return err
	}
	_, err = service.AddReadings("self-check", lot.ID, []app.AddReadingInput{{DeviceID: "probe", SampledAt: now, Celsius: 9, Source: "self-check", IdempotencyKey: "one"}, {DeviceID: "probe", SampledAt: now.Add(15 * time.Minute), Celsius: 10, Source: "self-check", IdempotencyKey: "two"}})
	if err != nil {
		return err
	}
	excursions, err := service.ListExcursions(lot.ID, false)
	if err != nil || len(excursions) == 0 {
		return fmt.Errorf("规则计算自检失败")
	}
	if !service.Store().Health().AuditValid {
		return fmt.Errorf("审计链自检失败")
	}
	manager := jobs.New(service, 4, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	cancel()
	manager.Wait()
	if manager.Healthy() {
		return fmt.Errorf("后台任务关闭自检失败")
	}
	reopened, err := store.Open(dataDir)
	if err != nil {
		return fmt.Errorf("仓储重放自检失败: %w", err)
	}
	recoveredService := app.New(reopened, clock.Real{})
	if _, err := recoveredService.GetPolicy(p.ID); err != nil {
		return fmt.Errorf("规则重放自检失败: %w", err)
	}
	if _, _, err := recoveredService.GetLot(lot.ID); err != nil {
		return fmt.Errorf("批次重放自检失败: %w", err)
	}
	recoveredExcursions, err := recoveredService.ListExcursions(lot.ID, false)
	if err != nil || len(recoveredExcursions) != len(excursions) || !reopened.Health().AuditValid {
		return fmt.Errorf("偏差与审计重放自检失败")
	}
	fmt.Printf("自检通过：地址=%s，规则=%s，批次=%s，偏差=%d\n", address, p.ID, lot.ID, len(excursions))
	return nil
}
