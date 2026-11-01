package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloudchat/internal/demo"
	"cloudchat/internal/loadbench"
)

func main() {
	var workload loadbench.Config
	var outputPath string
	var composeFile string
	var composeProject string
	var faultWindowFile string
	var readyFile string
	var settle time.Duration
	flag.StringVar(&workload.BaseURL, "base-url", "http://127.0.0.1:18080", "public API origin")
	flag.StringVar(&workload.GroupRoomID, "group-room-id", demo.GroupRoomID, "prepared group room UUID")
	flag.StringVar(&workload.PrivateRoomID, "private-room-id", demo.DirectRoomID, "prepared private room UUID")
	flag.StringVar(&workload.UserAID, "user-a-id", demo.UserAID, "prepared local test identity UUID")
	flag.StringVar(&workload.UserBID, "user-b-id", demo.UserBID, "second prepared local test identity UUID")
	flag.IntVar(&workload.Connections, "connections", 100, "concurrent WebSocket connections")
	flag.IntVar(&workload.MessagesPerSecond, "messages-per-second", 10, "aggregate HTTP message submission rate")
	flag.DurationVar(&workload.Duration, "duration", 60*time.Second, "load window")
	flag.IntVar(&workload.PayloadBytes, "payload-bytes", 256, "UTF-8 message text bytes")
	flag.IntVar(&workload.GroupPercent, "group-percent", 50, "share of submitted messages sent to the group room")
	flag.IntVar(&workload.GroupRoomSize, "group-room-size", 2, "prepared group membership count recorded in the report")
	flag.IntVar(&workload.PrivateRoomSize, "private-room-size", 2, "prepared private room membership count recorded in the report")
	flag.StringVar(&outputPath, "output", "", "write JSON report to this path")
	flag.StringVar(&composeFile, "compose-file", "", "Compose file for read-only Docker resource and outbox sampling")
	flag.StringVar(&composeProject, "compose-project", "", "Compose project; must be chat-ticket08-benchmark")
	flag.StringVar(&faultWindowFile, "fault-window", "", "JSON fault window written by the isolated scenario runner")
	flag.StringVar(&readyFile, "ready-file", "", "write a marker after all WebSocket subscriptions acknowledge")
	flag.DurationVar(&settle, "settle", 3*time.Second, "wait for live events before HTTP sync reconciliation")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := loadbench.Run(ctx, loadbench.RunOptions{
		Workload: workload, OutputPath: outputPath, ComposeFile: composeFile,
		ComposeProject: composeProject, FaultWindowFile: faultWindowFile, ReadyFile: readyFile,
		SettleDuration: settle,
	})
	if err != nil {
		log.Printf("load benchmark finished with sync error: %v", err)
	}
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		log.Fatalf("encode benchmark report: %v", marshalErr)
	}
	fmt.Println(string(encoded))
	if err != nil {
		os.Exit(1)
	}
}
