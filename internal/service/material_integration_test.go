package service

import (
	"context"
	"testing"

	"github.com/wyw14/solo-039/internal/material"
)

// TestMaterialBalanceLifecycle 走完整生命周期：开车建窗、读数核算、
// 换进料源切窗（边界读数两侧共享）、停机结账冻结、迟到读数修订保留原结论、
// 进程重启后从事件重建台账。
func TestMaterialBalanceLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	plant := NewPlant(dataDir)
	clock := int64(1000)
	plant.now = func() int64 { return clock }
	ctx := context.Background()

	if _, err := plant.Start(ctx, "baseline", "primary"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if plant.MaterialBalances()[0].StartAt != 1000 {
		t.Fatalf("window start mismatch")
	}
	mustRead := func(p material.Point, v float64, at int64) {
		t.Helper()
		if _, _, err := plant.RecordMaterialReading(string(p), v, at); err != nil {
			t.Fatalf("read %s@%d: %v", p, at, err)
		}
	}
	// 期初基准（t=1000）。
	mustRead(material.FeedIn, 0, 1000)
	mustRead(material.Overhead, 0, 1000)
	mustRead(material.Bottoms, 0, 1000)
	mustRead(material.Holdup, 10, 1000)

	// 窗口1期末（t=1100，换源时刻）：进料100、塔顶60、塔底35、
	// 存量 10->15（净积累5）=> 100-60-35-5=0 持平。
	clock = 1100
	mustRead(material.FeedIn, 100, 1100)
	mustRead(material.Overhead, 60, 1100)
	mustRead(material.Bottoms, 35, 1100)
	mustRead(material.Holdup, 15, 1100)

	// 换进料源：切开核算窗口，t=1100 边界读数结转为窗口2期初。
	if _, err := plant.Transition(ctx, "premium", "secondary"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if len(plant.MaterialBalances()) != 2 {
		t.Fatal("expected 2 windows after feed switch")
	}

	// 窗口2期末（t=1200，停机）：进料 180-100=80、塔顶 120-60=60、
	// 塔底 50-35=15、存量 15->20（净积累5）=> 80-60-15-5=0 持平。
	clock = 1200
	mustRead(material.FeedIn, 180, 1200)
	mustRead(material.Overhead, 120, 1200)
	mustRead(material.Bottoms, 50, 1200)
	mustRead(material.Holdup, 20, 1200)

	if _, err := plant.Shutdown(ctx, false); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	balances := plant.MaterialBalances()
	if len(balances) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(balances))
	}
	for i, b := range balances {
		if !b.Closed {
			t.Fatalf("window %d not closed", i)
		}
		if b.Verdict != material.VerdictBalanced || b.Loss != 0 {
			t.Fatalf("window %d expected balanced, got %s loss=%g", i, b.Verdict, b.Loss)
		}
	}

	// 迟到的修正值（t=1300 才报正 t=1100 的塔顶读数，实为 50 而非 60）：
	// 该观测位于切窗边界、两窗共享，覆盖后 W1 塔顶变为 50，
	// 损失 = 100-50-35-5 = 10；W2 期初同步变为 50、塔顶产出 70，
	// 损失 = 80-70-15-5 = -10（盘盈）。缺失量在时段间转移，两窗合计守恒。
	clock = 1300
	if _, windows, err := plant.RecordMaterialReading(string(material.Overhead), 50, 1100); err != nil {
		t.Fatalf("late reading: %v", err)
	} else if len(windows) != 2 {
		t.Fatalf("boundary revision should touch 2 windows, got %v", windows)
	}
	all := plant.MaterialBalances()
	b1, b2 := all[0], all[1]
	if b1.Verdict != material.VerdictLoss || b1.Loss != 10 {
		t.Fatalf("revised window1 expected loss 10, got %s %g", b1.Verdict, b1.Loss)
	}
	// 结账时冻结的原结论必须保留。
	if b1.OriginalVerdict != material.VerdictBalanced || b1.OriginalLoss != 0 {
		t.Fatalf("original conclusion not retained: %+v", b1)
	}
	if b1.ConclusionHeld {
		t.Fatal("verdict flipped but reported held")
	}
	if !b1.Amended || len(b1.Amendments) != 1 || b1.Amendments[0].Reason != "late-revision" {
		t.Fatalf("amendment trace missing: %+v", b1.Amendments)
	}
	if b2.Verdict != material.VerdictGain || b2.Loss != -10 {
		t.Fatalf("window2 should book the transferred gain, got %s %g", b2.Verdict, b2.Loss)
	}
	if b1.Loss+b2.Loss != 0 {
		t.Fatalf("mass not conserved across windows: %g + %g", b1.Loss, b2.Loss)
	}
	if b2.OriginalVerdict != material.VerdictBalanced {
		t.Fatalf("window2 original conclusion not retained: %+v", b2)
	}

	// 重启：从持久事件重建台账，修订与原结论应完整保留。
	restored := NewPlant(dataDir)
	if err := restored.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rb := restored.MaterialBalances()
	if len(rb) != 2 {
		t.Fatalf("restored windows=%d", len(rb))
	}
	got := rb[0]
	if got.Verdict != material.VerdictLoss || got.Loss != 10 ||
		got.OriginalVerdict != material.VerdictBalanced || !got.Amended || len(got.Amendments) != 1 {
		t.Fatalf("restored window1 lost revision state: %+v", got)
	}
	if rb[1].Verdict != material.VerdictGain || rb[1].Loss != -10 ||
		rb[1].OriginalVerdict != material.VerdictBalanced || len(rb[1].Amendments) != 1 {
		t.Fatalf("restored window2 lost revision state: %+v", rb[1])
	}
}
