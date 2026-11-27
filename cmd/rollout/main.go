package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"baremetal-rollout/internal/api"
	"baremetal-rollout/internal/fleet"
	"baremetal-rollout/internal/report"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	machines := flag.Int("machines", 800, "仿真服务器数量")
	seed := flag.Int64("seed", 20261127, "确定性随机种子")
	cli := flag.Bool("cli", false, "只运行命令行对比报告，不启动 Web 服务")
	flag.Parse()

	base := fleet.Generate(*machines, *seed)
	comparison, err := report.RunComparison(base, *seed)
	if err != nil {
		log.Fatalf("运行仿真对比失败: %v", err)
	}

	if *cli {
		printCLI(comparison)
		return
	}

	server, err := api.NewServer(base, comparison, *seed)
	if err != nil {
		log.Fatalf("初始化 API 失败: %v", err)
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("裸金属装机仿真与调度控制台已启动: http://localhost%s", portForLog(*addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

func portForLog(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return addr
	}
	return addr
}

func printCLI(c report.Comparison) {
	fmt.Println("== 800 台裸金属装机仿真：全开 vs 建议分批 ==")
	fmt.Printf("机型/机柜: %d 台；建议批次: %d 台/批，共 %d 批；上电速率 %.0f 台/秒\n",
		c.Total, c.Plan.BatchSize, c.Plan.BatchCount, c.Plan.PowerRatePerSec)
	printRun("一次全部上电", c.Storm)
	printRun("建议分批上电", c.Batched)
	fmt.Printf("成功数变化: %+d；不成功总数变化: %+d（分批-全开）\n", c.SuccessGain, -c.StormExtraFailures)
	fmt.Printf("分批方案真值核对: %d/%d = %.2f%%\n", c.BatchAccuracy.Matched, c.BatchAccuracy.Total, c.BatchAccuracy.Accuracy*100)
	fmt.Println("真值分布:", c.BatchAccuracy.ByTruth)
}

func printRun(name string, r report.RunReport) {
	fmt.Printf("\n[%s] 仿真总耗时 %s；成功 %d，预检/配置阻塞 %d，拥塞失败 %d，未完成 %d\n",
		name, formatMS(r.DurationMS), r.Success, r.Blocked, r.Failed, r.Incomplete)
	fmt.Printf("  归因计数: %v\n", r.ReasonCounts)
	for _, b := range r.Bottlenecks {
		fmt.Printf("  瓶颈 %-16s peak=%.0f safe=%.0f util=%.2f timeouts=%d severity=%s\n",
			b.Service, b.PeakLoad, b.SafeLoad, b.Utilization, b.Timeouts, b.Severity)
	}
}

func formatMS(ms int64) string {
	sec := ms / 1000
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}
