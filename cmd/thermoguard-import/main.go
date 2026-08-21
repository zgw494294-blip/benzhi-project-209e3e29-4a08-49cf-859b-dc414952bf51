package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"thermoguard/internal/app"
	"thermoguard/internal/clock"
	"thermoguard/internal/importer"
	"thermoguard/internal/store"
)

type summary struct {
	LotID      string `json:"lot_id"`
	Total      int    `json:"total"`
	Created    int    `json:"created"`
	Duplicates int    `json:"duplicates"`
	DryRun     bool   `json:"dry_run"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "导入失败：", err)
		os.Exit(1)
	}
}
func run() error {
	dataDir := flag.String("data-dir", "./var/data", "数据目录")
	lotID := flag.String("lot", "", "批次编号")
	file := flag.String("file", "", "NDJSON 文件")
	actor := flag.String("actor", "", "行为人")
	dryRun := flag.Bool("dry-run", false, "仅校验不写入")
	flag.Parse()
	if *lotID == "" || *file == "" || *actor == "" {
		return fmt.Errorf("必须提供 -lot、-file 和 -actor")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	inputs, err := importer.ParseNDJSON(f, importer.ParseOptions{})
	if err != nil {
		return err
	}
	repo, err := store.Open(*dataDir)
	if err != nil {
		return err
	}
	service := app.New(repo, clock.Real{})
	if _, _, err := service.GetLot(*lotID); err != nil {
		return err
	}
	out := summary{LotID: *lotID, Total: len(inputs), DryRun: *dryRun}
	if *dryRun {
		validation, err := service.ValidateReadings(*lotID, inputs)
		if err != nil {
			return err
		}
		out.Created = validation.NewCount
		out.Duplicates = validation.DuplicateCount
	} else {
		results, err := service.AddReadings(*actor, *lotID, inputs)
		if err != nil {
			return err
		}
		for _, result := range results {
			if result.Duplicate {
				out.Duplicates++
			} else {
				out.Created++
			}
		}
	}
	data, _ := json.Marshal(out)
	fmt.Printf("导入完成：总计 %d，新增 %d，重复 %d\n%s\n", out.Total, out.Created, out.Duplicates, data)
	return nil
}
