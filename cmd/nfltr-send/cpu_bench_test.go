// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"bufio"
	"flag"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/internal/xfer"
)

var transferMB = flag.Int("transfer-mb", 256, "BenchmarkDirectCPU: MiB per transfer")

func cpuTime() time.Duration {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}

// BenchmarkDirectCPU: one file of -transfer-mb, served with no account and
// received over the direct path (loopback), sender and receiver in this
// process; reports both ends' CPU seconds per GiB (getrusage) and the rate.
// bench/cpu/netem.sh runs two builds of it interleaved behind netem (lane
// SENDCPU), so a CPU difference between builds is measured, not inferred
// from cloud runs on different machines.
func BenchmarkDirectCPU(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "offer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "big"))
	if err != nil {
		b.Fatal(err)
	}
	rng, buf := rand.NewChaCha8([32]byte{1}), make([]byte, 1<<20)
	for range *transferMB {
		_, _ = rng.Read(buf)
		if _, err := f.Write(buf); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	r := anonRelay(b, true)
	src, err := xfer.NewSource([]string{dir})
	if err != nil {
		b.Fatal(err)
	}
	var cpu, wall time.Duration
	for b.Loop() {
		c0, w0 := cpuTime(), time.Now()
		pr, pw := io.Pipe()
		go func() {
			serve(b.Context(), nfnet.Config{Relay: "relay.test", AgentRelay: r.GRPC, Plaintext: true}, nfnet.DirectOn, nil, src, true, pw, io.Discard)
			_ = pw.Close()
		}()
		addr, err := bufio.NewReader(pr).ReadString('\n')
		if err != nil {
			b.Fatal(err)
		}
		go func() { _, _ = io.Copy(io.Discard, pr) }()
		if code := receiveFrom(b, r, addr[:len(addr)-1], nfnet.DirectOn, b.TempDir()); code != 0 {
			b.Fatalf("receive exit %d", code)
		}
		cpu, wall = cpu+cpuTime()-c0, wall+time.Since(w0)
	}
	gib := float64(*transferMB) / 1024 * float64(b.N)
	b.ReportMetric(cpu.Seconds()/gib, "cpu-s/GiB")
	b.ReportMetric(gib*8*1024*1.048576/wall.Seconds(), "Mbit/s")
}
