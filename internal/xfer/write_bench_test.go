// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package xfer

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func cpuTime() time.Duration {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}

// BenchmarkStripedWrite: 4 writers write interleaved 64 MiB runs of chunks
// into one 1 GiB file, as a striped receive does: "plain" each WriteAt at
// once (the kernel's inode lock spins), "receive" the receive's way
// (preallocate, writeChunk). CPU s per GiB from the process's CPU time
// (cpuTime); lane SPEED-RECV's guard of nfltr docs/performance-ledger.md
// (box, ext4: plain 1.47, receive 0.58).
func BenchmarkStripedWrite(b *testing.B) {
	const size, run, k = 1 << 30, 64 << 20, 4
	for _, mode := range []string{"plain", "receive"} {
		b.Run(mode, func(b *testing.B) {
			r := &receiver{m: Manifest{Entries: []Entry{{Path: "f", Size: size}}}}
			for range b.N {
				f, err := os.Create(filepath.Join(b.TempDir(), "f"))
				if err != nil {
					b.Fatal(err)
				}
				if mode == "receive" {
					preallocate(f, size)
				}
				var wg sync.WaitGroup
				c0, t0 := cpuTime(), time.Now()
				for w := range k {
					wg.Go(func() {
						p := make([]byte, maxData)
						for off := int64(w) * run; off < size; off += k * run {
							for c := off; c < off+run; c += maxData {
								var err error
								if mode == "receive" {
									err = r.writeChunk(0, f, p, c)
								} else {
									_, err = f.WriteAt(p, c)
								}
								if err != nil {
									panic(err)
								}
							}
						}
					})
				}
				wg.Wait()
				b.ReportMetric((cpuTime() - c0).Seconds(), "cpu-s/GiB")
				b.ReportMetric(float64(size)/(1<<20)/time.Since(t0).Seconds(), "MiB/s")
				_ = f.Close()
			}
		})
	}
}
