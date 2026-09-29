package material

import (
	"testing"

	"github.com/google/uuid"
)

func newTestLedger(t *testing.T) (*Ledger, uuid.UUID, uuid.UUID) {
	t.Helper()
	l := NewLedger()
	campaignID := uuid.New()
	revision := uuid.New()
	feedRevision := uuid.New()
	if _, err := l.Begin(campaignID, revision, "primary", feedRevision, 100, ReasonStart); err != nil {
		t.Fatalf("begin: %v", err)
	}
	return l, campaignID, revision
}

// record 辅助：登记 t 时刻读数。
func record(t *testing.T, l *Ledger, p Point, v float64, at int64) Reading {
	t.Helper()
	r, _, err := l.RecordReading(p, v, at, at+1)
	if err != nil {
		t.Fatalf("record %s@%d=%g: %v", p, at, v, err)
	}
	return r
}

func TestBalanceComputesNetAccumulationAndLoss(t *testing.T) {
	l, _, _ := newTestLedger(t)

	// 期初基准（t=100）：进料 0、塔顶 0、塔底 0、设备存量 10。
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	record(t, l, Holdup, 10, 100)
	// 期末（t=200）：进料 100、塔顶 60、塔底 35、存量 15（净积累 5）。
	record(t, l, FeedIn, 100, 200)
	record(t, l, Overhead, 60, 200)
	record(t, l, Bottoms, 35, 200)
	record(t, l, Holdup, 15, 200)

	b, ok := l.ActiveBalance()
	if !ok {
		t.Fatal("active balance missing")
	}
	if b.Received != 100 || b.Overhead != 60 || b.Bottoms != 35 || b.NetAccumulation != 5 {
		t.Fatalf("unexpected balance terms: %+v", b)
	}
	// 损失 = 100 - 60 - 35 - 5 = 0
	if b.Loss != 0 || b.Verdict != VerdictBalanced {
		t.Fatalf("expected balanced, got %s loss=%g", b.Verdict, b.Loss)
	}
}

func TestBalanceDetectsLoss(t *testing.T) {
	l, _, _ := newTestLedger(t)
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	record(t, l, Holdup, 10, 100)
	record(t, l, FeedIn, 100, 200)
	record(t, l, Overhead, 55, 200)
	record(t, l, Bottoms, 30, 200)
	record(t, l, Holdup, 10, 200) // 净积累 0

	b, _ := l.ActiveBalance()
	// 损失 = 100 - 55 - 30 - 0 = 15
	if b.Loss != 15 || b.Verdict != VerdictLoss {
		t.Fatalf("expected loss 15, got %s loss=%g", b.Verdict, b.Loss)
	}
}

func TestIncompleteUntilAllPointsHaveSpan(t *testing.T) {
	l, _, _ := newTestLedger(t)
	record(t, l, FeedIn, 0, 100)
	b, _ := l.ActiveBalance()
	if b.Verdict != VerdictIncomplete {
		t.Fatalf("expected incomplete, got %s", b.Verdict)
	}
	if len(b.Missing) != 4 {
		t.Fatalf("expected 4 missing points, got %v", b.Missing)
	}
}

func TestFeedSwitchSplitsWindowWithSharedBoundary(t *testing.T) {
	l, campaignID, _ := newTestLedger(t)

	// 窗口1：t=100 基准，t=200 期末。进料100、塔顶60、塔底35、存量 10->15（积累5）=> 持平。
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	record(t, l, Holdup, 10, 100)
	record(t, l, FeedIn, 100, 200)
	record(t, l, Overhead, 60, 200)
	record(t, l, Bottoms, 35, 200)
	record(t, l, Holdup, 15, 200)

	rev2 := uuid.New()
	feedRev2 := uuid.New()
	if _, err := l.Begin(campaignID, rev2, "secondary", feedRev2, 200, ReasonFeedSwitch); err != nil {
		t.Fatalf("begin window2: %v", err)
	}

	// 窗口2 以 t=200 读数结转作为期初基准。
	record(t, l, FeedIn, 180, 300)
	record(t, l, Overhead, 120, 300)
	record(t, l, Bottoms, 50, 300)
	record(t, l, Holdup, 20, 300) // 15->20，净积累 5

	balances := l.Balances()
	if len(balances) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(balances))
	}
	w1, w2 := balances[0], balances[1]
	if !w1.Closed || w1.FeedSource != "primary" || w2.FeedSource != "secondary" {
		t.Fatalf("window split wrong: %+v %+v", w1, w2)
	}
	if w1.Verdict != VerdictBalanced || w1.Loss != 0 {
		t.Fatalf("window1 should be balanced: %s %g", w1.Verdict, w1.Loss)
	}
	// 窗口2：进料 80、塔顶 60、塔底 15、积累 5 => 损失 0
	if w2.Received != 80 || w2.Overhead != 60 || w2.Bottoms != 15 || w2.NetAccumulation != 5 {
		t.Fatalf("window2 terms wrong: %+v", w2)
	}
	if w2.Verdict != VerdictBalanced {
		t.Fatalf("window2 should be balanced: %s %g", w2.Verdict, w2.Loss)
	}
	if w2.Revision != rev2 {
		t.Fatal("window2 revision not advanced")
	}
}

func TestShutdownClosesWindowAndFreezesConclusion(t *testing.T) {
	l, _, _ := newTestLedger(t)
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	record(t, l, Holdup, 10, 100)
	record(t, l, FeedIn, 100, 200)
	record(t, l, Overhead, 55, 200)
	record(t, l, Bottoms, 30, 200)
	record(t, l, Holdup, 10, 200) // 结账时：损失 15

	closed, err := l.Close(200)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !closed.Closed || closed.ClosedReason != ReasonShutdown {
		t.Fatalf("window not closed by shutdown: %+v", closed)
	}
	if closed.OriginalVerdict != VerdictLoss || closed.OriginalLoss != 15 {
		t.Fatalf("original conclusion not frozen: %+v", closed)
	}
	if _, ok := l.ActiveBalance(); ok {
		t.Fatal("no active window expected after shutdown")
	}

	// 迟到的修正值：t=200 塔顶表实为 70（原报 55），覆盖同时刻观测后衡算持平。
	r, _, err := l.RecordReading(Overhead, 70, 200, 250)
	if err != nil {
		t.Fatalf("late reading: %v", err)
	}
	b := l.Balances()[0]
	if !b.Amended || len(b.Amendments) != 1 {
		t.Fatalf("expected one amendment, got %+v", b.Amendments)
	}
	am := b.Amendments[0]
	if am.Seq != r.Seq || am.Reason != "late-revision" || am.Recorded != 250 {
		t.Fatalf("amendment trace wrong: %+v", am)
	}
	if am.PreviousVerdict != VerdictLoss || am.PreviousLoss != 15 {
		t.Fatalf("amendment must capture pre-revision conclusion: %+v", am)
	}
	// 重算后当期数值：损失 100-70-30-0 = 0，结论翻为 balanced。
	if b.Loss != 0 || b.Verdict != VerdictBalanced {
		t.Fatalf("revised balance wrong: %s %g", b.Verdict, b.Loss)
	}
	// 原结论保留，且结论发生翻转时 ConclusionHeld=false。
	if b.OriginalVerdict != VerdictLoss || b.OriginalLoss != 15 {
		t.Fatalf("original conclusion lost after revision: %+v", b)
	}
	if b.ConclusionHeld {
		t.Fatal("conclusion changed but reported as held")
	}
}

func TestLateReadingThatConfirmsLossKeepsConclusion(t *testing.T) {
	l, _, _ := newTestLedger(t)
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	record(t, l, Holdup, 10, 100)
	record(t, l, FeedIn, 100, 200)
	record(t, l, Overhead, 55, 200)
	record(t, l, Bottoms, 30, 200)
	record(t, l, Holdup, 10, 200)
	if _, err := l.Close(200); err != nil {
		t.Fatal(err)
	}

	// 迟到读数仅小幅修正，损失依旧存在（塔顶 55 -> 56，损失 14）。
	if _, _, err := l.RecordReading(Overhead, 56, 200, 240); err != nil {
		t.Fatal(err)
	}
	b := l.Balances()[0]
	if b.Verdict != VerdictLoss || b.Loss != 14 {
		t.Fatalf("expected persistent loss, got %s %g", b.Verdict, b.Loss)
	}
	if !b.ConclusionHeld {
		t.Fatal("same verdict should be reported as conclusion held")
	}
}

func TestCorrectionRevisesBothWindowsViaSharedSeq(t *testing.T) {
	l, campaignID, _ := newTestLedger(t)
	// 窗口1
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	holdBoundary := record(t, l, Holdup, 10, 100)
	record(t, l, FeedIn, 100, 200)
	record(t, l, Overhead, 60, 200)
	record(t, l, Bottoms, 35, 200)
	record(t, l, Holdup, 15, 200)
	// 切窗：t=200 读数结转到窗口2
	if _, err := l.Begin(campaignID, uuid.New(), "secondary", uuid.New(), 200, ReasonFeedSwitch); err != nil {
		t.Fatal(err)
	}
	record(t, l, FeedIn, 180, 300)
	record(t, l, Overhead, 120, 300)
	record(t, l, Bottoms, 55, 300)
	record(t, l, Holdup, 20, 300)
	// 停机结账（两窗原本都持平）
	if _, err := l.Close(300); err != nil {
		t.Fatal(err)
	}

	// 更正窗口1的期初存量读数（Seq 同时存在于窗口1，因 At=100 不结转）。
	affected, err := l.CorrectReading(holdBoundary.Seq, 8, 400, "gauge recalibration")
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if len(affected) != 1 {
		t.Fatalf("expected 1 affected window, got %d", len(affected))
	}
	// 窗口1 净积累变为 15-8=7，损失 = 100-60-35-7 = -2（盘盈）。
	b1 := l.Balances()[0]
	if b1.NetAccumulation != 7 || b1.Verdict != VerdictGain || b1.Loss != -2 {
		t.Fatalf("window1 revision wrong: %+v", b1)
	}
	if len(b1.Amendments) != 1 || b1.Amendments[0].Reason != "gauge recalibration" {
		t.Fatalf("correction trace missing: %+v", b1.Amendments)
	}
	if b1.OriginalVerdict != VerdictBalanced {
		t.Fatalf("original conclusion must be retained: %+v", b1)
	}

	// 更正边界 t=200 的存量读数（15->12），该读数为两窗共享，应同时修订。
	var boundarySeq int64
	for _, r := range l.readingsOfPublic(Holdup) {
		if r.At == 200 {
			boundarySeq = r.Seq
		}
	}
	if boundarySeq == 0 {
		t.Fatal("boundary reading not found")
	}
	affected, err = l.CorrectReading(boundarySeq, 12, 400, "transmitter correction")
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 2 {
		t.Fatalf("shared boundary should revise 2 windows, got %d", len(affected))
	}
}

// readingsOfPublic 暴露内部读数以便测试定位 Seq。
func (l *Ledger) readingsOfPublic(p Point) []Reading {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Reading, 0)
	for _, r := range l.readings {
		if r.Point == p {
			out = append(out, r)
		}
	}
	return out
}

func TestReadingValidation(t *testing.T) {
	l, _, _ := newTestLedger(t)
	if _, _, err := l.RecordReading("vapor", 1, 100, 101); err == nil {
		t.Fatal("unknown point accepted")
	}
	if _, _, err := l.RecordReading(FeedIn, -1, 100, 101); err == nil {
		t.Fatal("negative reading accepted")
	}
	if _, _, err := l.RecordReading(FeedIn, 1, 0, 101); err == nil {
		t.Fatal("zero timestamp accepted")
	}
}

func TestRejectsReadingOutsideWindows(t *testing.T) {
	l, _, _ := newTestLedger(t) // 窗口起点 100
	if _, _, err := l.RecordReading(FeedIn, 0, 50, 51); err == nil {
		t.Fatal("reading before first window accepted")
	}
	if _, err := l.Close(200); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.RecordReading(FeedIn, 1, 250, 251); err == nil {
		t.Fatal("reading after shutdown accepted")
	}
}

func TestRestartAfterShutdownOpensFreshWindow(t *testing.T) {
	l, campaignID, rev1 := newTestLedger(t)
	record(t, l, FeedIn, 0, 100)
	record(t, l, Overhead, 0, 100)
	record(t, l, Bottoms, 0, 100)
	record(t, l, Holdup, 10, 100)
	if _, err := l.Close(150); err != nil {
		t.Fatal(err)
	}
	// 重新开车：边界不得早于上次停机时刻。
	if _, err := l.Begin(campaignID, uuid.New(), "primary", uuid.New(), 149, ReasonStart); err == nil {
		t.Fatal("boundary before shutdown accepted")
	}
	b, err := l.Begin(campaignID, rev1, "primary", uuid.New(), 200, ReasonStart)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if b.Closed {
		t.Fatal("new window should be open")
	}
	if len(l.Balances()) != 2 {
		t.Fatal("expected two windows across restart")
	}
	// 停机后重新开车不跨停产间隔结转读数，新窗口从零开始。
	if b.ReadingCount != 0 {
		t.Fatalf("fresh window must not inherit readings across shutdown, got %d", b.ReadingCount)
	}
	// 停产间隔内的读数只能修订旧窗口，不会进入新窗口（仅命中 W001）。
	if _, windows, err := l.RecordReading(Overhead, 5, 150, 180); err != nil {
		t.Fatalf("gap reading should revise the closed window: %v", err)
	} else if len(windows) != 1 || windows[0] != "W001" {
		t.Fatalf("gap reading must only touch W001, got %v", windows)
	}
}
