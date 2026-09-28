// Package profiling writes Go runtime profiles when WOPR_PROFILE names them.
//
// With WOPR_PROFILE unset, Start performs one environment lookup and returns a
// no-op, so release binaries pay nothing for the capability. With it set, wopr
// profiles the whole process and writes the files on exit:
//
//	WOPR_PROFILE=cpu,heap wopr -p "hello"
//	WOPR_PROFILE=trace WOPR_PROFILE_DIR=/tmp/wopr-profile wopr
//
// Kinds are cpu, heap, allocs, block, mutex, goroutine, and trace. Files go to
// WOPR_PROFILE_DIR (default: the current directory) as wopr-<pid>-<kind>.pprof,
// or wopr-<pid>.trace for the execution trace. Read them with `go tool pprof`
// and `go tool trace`, or run `make profile` from a source checkout.
package profiling

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strings"
	"sync"
)

// Kinds lists the accepted WOPR_PROFILE values in the order they are written.
var Kinds = []string{"cpu", "heap", "allocs", "block", "mutex", "goroutine", "trace"}

// Start begins the profiles named by WOPR_PROFILE and returns the function that
// stops them and writes every file. The returned function is safe to call more
// than once; only the first call writes.
func Start() (stop func()) {
	spec := os.Getenv("WOPR_PROFILE")
	if spec == "" {
		return func() {}
	}
	return StartWith(spec, os.Getenv("WOPR_PROFILE_DIR"), os.Stderr)
}

// StartWith is Start with explicit inputs. Problems are reported to diag and
// never stop wopr: a profile that cannot be written is skipped.
func StartWith(spec, dir string, diag io.Writer) (stop func()) {
	if dir == "" {
		dir = "."
	}
	kinds := map[string]bool{}
	for kind := range strings.SplitSeq(spec, ",") {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if !slices.Contains(Kinds, kind) {
			warnf(diag, "wopr: ignoring unknown WOPR_PROFILE kind %q (valid: %s)\n", kind, strings.Join(Kinds, ", "))
			continue
		}
		kinds[kind] = true
	}
	if len(kinds) == 0 {
		return func() {}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		warnf(diag, "wopr: WOPR_PROFILE_DIR: %v\n", err)
		return func() {}
	}
	prefix := filepath.Join(dir, fmt.Sprintf("wopr-%d", os.Getpid()))
	var cpuFile, traceFile *os.File
	if kinds["cpu"] {
		cpuFile = create(prefix+"-cpu.pprof", diag)
		if cpuFile != nil {
			if err := pprof.StartCPUProfile(cpuFile); err != nil {
				warnf(diag, "wopr: cpu profile: %v\n", err)
				_ = cpuFile.Close() // The profile never started; the empty file carries nothing.
				cpuFile = nil
			}
		}
	}
	if kinds["trace"] {
		traceFile = create(prefix+".trace", diag)
		if traceFile != nil {
			if err := trace.Start(traceFile); err != nil {
				warnf(diag, "wopr: trace: %v\n", err)
				_ = traceFile.Close() // The trace never started; the empty file carries nothing.
				traceFile = nil
			}
		}
	}
	if kinds["block"] {
		runtime.SetBlockProfileRate(1)
	}
	if kinds["mutex"] {
		runtime.SetMutexProfileFraction(1)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			var written []string
			if cpuFile != nil {
				pprof.StopCPUProfile()
				if err := cpuFile.Close(); err != nil {
					warnf(diag, "wopr: cpu profile: %v\n", err)
				} else {
					written = append(written, cpuFile.Name())
				}
			}
			if traceFile != nil {
				trace.Stop()
				if err := traceFile.Close(); err != nil {
					warnf(diag, "wopr: trace: %v\n", err)
				} else {
					written = append(written, traceFile.Name())
				}
			}
			for _, kind := range []string{"heap", "allocs", "block", "mutex", "goroutine"} {
				if !kinds[kind] {
					continue
				}
				if kind == "heap" {
					runtime.GC() // Report live objects as of exit, not the last GC cycle.
				}
				path := prefix + "-" + kind + ".pprof"
				if f := create(path, diag); f != nil {
					err := pprof.Lookup(kind).WriteTo(f, 0)
					if closeErr := f.Close(); err == nil {
						err = closeErr
					}
					if err != nil {
						warnf(diag, "wopr: %s profile: %v\n", kind, err)
					} else {
						written = append(written, path)
					}
				}
			}
			for _, path := range written {
				warnf(diag, "wopr: wrote %s\n", path)
			}
		})
	}
}

func create(path string, diag io.Writer) *os.File {
	f, err := os.Create(path)
	if err != nil {
		warnf(diag, "wopr: %v\n", err)
		return nil
	}
	return f
}

// warnf reports a best-effort diagnostic. A failed diagnostic write has no
// better channel, so it is dropped.
func warnf(diag io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(diag, format, args...)
}
