package main

// W7: running a recipe. The runner is a method on App so it reuses the same
// connection registry, backup path and sync/transfer orchestrators the UI calls
// — a scheduled job is the app, minus the window.

import (
	"fmt"
	"log"
	"time"
)

// RunJob executes a recipe against the app's saved connections and returns the
// per-step result.
//
// It does NOT connect anything itself: the caller connects the saved
// connections first (main.go does, the same way the MCP path does), so a job and
// an MCP session share one definition of "the connections this process knows".
func (a *App) RunJob(recipe *JobRecipe, dryRun bool, clock jobClock) JobResult {
	if clock == nil {
		clock = defaultJobClock
	}
	started := time.Now()
	result := JobResult{
		Name:       recipe.Name,
		Steps:      make([]JobStepResult, 0, len(recipe.Steps)),
		OK:         true,
		FailedStep: -1,
	}

	for i, step := range recipe.Steps {
		label := stepLabel(step, i)
		sr := JobStepResult{Index: i, Kind: step.Kind, Label: label}

		if dryRun {
			// A dry run validates and reports the plan WITHOUT writing: the
			// transfer and rowsync previews are read-only, so they are the
			// natural "what would this do" for those steps. Backup has no
			// preview (it shells out to pg_dump), so a dry run only reports that
			// it would run.
			sr.OK = true
			sr.Detail = "dry-run：已校验，未执行"
			switch step.Kind {
			case "transfer":
				preview, err := a.PreviewTransfer(*step.Transfer)
				if err != nil {
					sr.OK = false
					sr.Error = redactForLog(err.Error())
				} else {
					sr.Detail = fmt.Sprintf("dry-run：将复制 %d 张表（约 %d 行）、%d 条 DDL",
						len(preview.Tables), preview.TotalRows, len(preview.Statements))
				}
			case "rowsync":
				preview, err := a.RowSyncPreview(step.RowSync.RowSyncRequest)
				if err != nil {
					sr.OK = false
					sr.Error = redactForLog(err.Error())
				} else {
					verb := "分析"
					if step.RowSync.Apply {
						verb = "将应用"
					}
					sr.Detail = fmt.Sprintf("dry-run：%s 新增 %d / 修改 %d / 删除 %d", verb,
						preview.Inserts, preview.Updates, preview.Deletes)
				}
			case "wait":
				sr.Detail = fmt.Sprintf("dry-run：将等待 %d 秒", step.Wait.Seconds)
			}
		} else {
			sr = a.runJobStep(i, step, clock, sr)
		}

		result.Steps = append(result.Steps, sr)
		log.Printf("job %q 步骤 %s: ok=%v %s", recipe.Name, label, sr.OK, redactForLog(sr.Detail))
		if !sr.OK {
			result.OK = false
			result.FailedStep = i
			break
		}
	}
	result.Elapsed = time.Since(started).String()
	return result
}

// runJobStep executes one step. A step that fails reports the failure; the
// caller stops the job there.
func (a *App) runJobStep(i int, step JobStep, clock jobClock, sr JobStepResult) JobStepResult {
	switch step.Kind {
	case "backup":
		res, err := a.Backup(*step.Backup)
		if err != nil {
			sr.OK = false
			sr.Error = redactForLog(err.Error())
			return sr
		}
		if !res.OK {
			sr.OK = false
			sr.Error = redactForLog(res.Error)
			return sr
		}
		sr.OK = true
		sr.Detail = fmt.Sprintf("已备份到 %s（%d 行日志）", step.Backup.OutPath, len(res.Log))
		return sr

	case "transfer":
		res, err := a.ApplyTransfer(*step.Transfer)
		if err != nil {
			sr.OK = false
			sr.Error = redactForLog(err.Error())
			return sr
		}
		if res.Aborted {
			sr.OK = false
			sr.Error = redactForLog(res.Error)
			return sr
		}
		sr.OK = true
		sr.Detail = fmt.Sprintf("复制 %d 张表 %d 行 · %s", len(res.Tables), res.Rows, res.Elapsed)
		return sr

	case "rowsync":
		preview, err := a.RowSyncPreview(step.RowSync.RowSyncRequest)
		if err != nil {
			sr.OK = false
			sr.Error = redactForLog(err.Error())
			return sr
		}
		summary := fmt.Sprintf("新增 %d / 修改 %d / 删除 %d", preview.Inserts, preview.Updates, preview.Deletes)
		if !step.RowSync.Apply {
			// Analyse-only: a report step. Drift is NOT a failure — a nightly job
			// that fails on drift cannot be used to measure drift.
			sr.OK = true
			sr.Detail = "仅分析（未写入）：" + summary
			return sr
		}
		seqs := step.RowSync.Seqs
		if len(seqs) == 0 {
			// "Apply everything the plan found" is the nightly-sync default.
			seqs = make([]int, 0, len(preview.Sample))
			for _, c := range preview.Sample {
				seqs = append(seqs, c.Seq)
			}
			if preview.Truncated {
				// The plan exceeds the preview sample; applying only the sample
				// would look successful while leaving the rest unsynced. Refuse.
				sr.OK = false
				sr.Error = fmt.Sprintf("计划有 %d 条变更，超过单次可应用上限 %d：请用数据传输整体复制",
					preview.Total, len(preview.Sample))
				return sr
			}
		}
		res, err := a.RowSyncApply(RowSyncApplyRequest{
			PlanID: preview.PlanID, Seqs: seqs, Confirm: true,
		})
		if err != nil {
			sr.OK = false
			sr.Error = redactForLog(err.Error())
			return sr
		}
		if res.Error != "" {
			sr.OK = false
			sr.Error = redactForLog(res.Error)
			return sr
		}
		sr.OK = true
		sr.Detail = fmt.Sprintf("已应用 %d 条（%s）· %s", res.Applied, summary, res.Elapsed)
		return sr

	case "wait":
		clock(time.Duration(step.Wait.Seconds) * time.Second)
		sr.OK = true
		sr.Detail = fmt.Sprintf("已等待 %d 秒", step.Wait.Seconds)
		return sr

	default:
		sr.OK = false
		sr.Error = fmt.Sprintf("未知的步骤类型 %q", step.Kind)
		return sr
	}
}

// jobExitCode maps a finished job to a process exit code.
//
// 0 = every step ran; 1 = a step failed; 2 = the recipe itself was unusable
// (handled by the caller before RunJob). The distinction matters because a
// scheduler's alert says only "exit 1": conflating a typo in the recipe with a
// database failure sends the operator to the wrong place.
func jobExitCode(res JobResult) int {
	if res.OK {
		return 0
	}
	return 1
}
