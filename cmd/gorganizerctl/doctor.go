package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/diag"
)

type doctorDeps struct {
	out     io.Writer
	errOut  io.Writer
	options diag.Options
}

// doctorOptions supplies local paths and a bounded Health check to the diagnostic runner.
func doctorOptions() diag.Options {
	self, _ := os.Executable()
	checkout := os.Getenv("GORGANIZER_ROOT")
	if checkout == "" && self != "" {
		candidate := filepath.Dir(self)
		if info, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil && info.Mode().IsRegular() {
			checkout = candidate
		}
	}
	return diag.Options{
		Version: version, Executable: self, Checkout: checkout, ProcRoot: "/proc",
		Health: func(ctx context.Context) (diag.Health, error) {
			conn, err := dialDaemon("")
			if err != nil {
				return diag.Health{}, err
			}
			defer conn.Close()
			response, err := pb.NewGorganizerClient(conn).Health(ctx, &pb.HealthRequest{})
			if err != nil {
				return diag.Health{}, err
			}
			return diag.Health{Version: response.GetVersion(), InstanceID: response.GetInstanceId(), Stopping: response.GetStopping()}, nil
		},
	}
}

// runDoctor prints a read-only diagnosis of the local installation.
func runDoctor(args []string) int {
	return runDoctorWith(args, doctorDeps{out: os.Stdout, errOut: os.Stderr, options: doctorOptions()})
}

// runDoctorWith prints checks using the supplied isolated environment.
func runDoctorWith(args []string, deps doctorDeps) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	report := diag.Run(deps.options)
	fmt.Fprint(deps.out, report.Text())
	if report.HasProblems() {
		return 1
	}
	return 0
}

// runBugReport collects a redacted local archive without uploading it.
func runBugReport(args []string) int {
	return runBugReportWith(args, doctorDeps{out: os.Stdout, errOut: os.Stderr, options: doctorOptions()})
}

// runBugReportWith creates a private report using the supplied isolated environment.
func runBugReportWith(args []string, deps doctorDeps) int {
	fs := flag.NewFlagSet("bug-report", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	out := fs.String("out", "", "folder for the report")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	path, err := diag.CreateBundle(diag.BundleOptions{OutDir: *out, Version: deps.options.Version, Checkout: deps.options.Checkout, Report: diag.Run(deps.options), Now: time.Now()})
	if err != nil {
		fmt.Fprintln(deps.errOut, "Could not create the report. Check the destination folder and try again.")
		return 1
	}
	fmt.Fprintln(deps.out, path)
	fmt.Fprintln(deps.out, "Nothing was uploaded. Attach this file to your report if you want to share it.")
	return 0
}
