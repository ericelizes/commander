package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/reviewrun"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// runWorkflowReviewReportCheck is the review Operator's last step before it
// posts its report and exits: the close door's format and provenance rules, run
// on the text, so a review is never redone for a missing Head line or receipt.
// Read-only: it writes nothing.
func runWorkflowReviewReportCheck(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("workflow review-report-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	head := fs.String("head", "", "full sha of the head you reviewed")
	file := fs.String("file", "-", "the report text (- = stdin)")
	receipt := fs.String("receipt", "", "the separate receipt comment (a file); empty when it sits in the report")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: cmdr workflow review-report-check --head <full-sha> [--file report.md|-] [--receipt receipt.txt]")
		return 2
	}
	report, err := readInput(*file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var rcpt string
	if *receipt != "" {
		if rcpt, err = readInput(*receipt); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	registry := filepath.Join(agentrt.CommanderHome(), "review-executions")
	verdict, err := workflowtemplate.CheckReport(report, rcpt, *head, func(run int64, manifest string) (reviewrun.Recorded, error) {
		return reviewrun.ReadRecorded(registry, run, manifest)
	})
	if err != nil {
		fmt.Fprintf(stderr, "report refused, fix it before posting: %v\n", err)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": true, "head": *head, "verdict": verdict})
	return 0
}

func readInput(name string) (string, error) {
	if name == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(name)
	return string(b), err
}
