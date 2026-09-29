package materialbalance

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

func mustOpen(t *testing.T, b *Book, at int64, source string) {
	t.Helper()
	if _, err := b.OpenWindow(at, source, uuid.New(), model.NewCampaign("g")); err != nil {
		t.Fatalf("open window source=%s: %v", source, err)
	}
}

func mustAdd(t *testing.T, b *Book, p Point, value float64, at int64) Acknowledgement {
	t.Helper()
	ack, err := b.Add(Reading{Point: p, Value: value, At: at})
	if err != nil {
		t.Fatalf("add %s@%d=%v: %v", p, at, value, err)
	}
	return ack
}

// fillBalancedWindow 报入一组完整读数：进料差 1000、塔顶 700、塔底 250、存量 +30，损失 20。
func fillBalancedWindow(t *testing.T, b *Book, t0, t1 int64) {
	t.Helper()
	mustAdd(t, b, PointFeed, 0, t0)
	mustAdd(t, b, PointOverhead, 0, t0)
	mustAdd(t, b, PointBottoms, 0, t0)
	mustAdd(t, b, PointInventory, 120, t0)
	mustAdd(t, b, PointFeed, 1000, t1)
	mustAdd(t, b, PointOverhead, 700, t1)
	mustAdd(t, b, PointBottoms, 250, t1)
	mustAdd(t, b, PointInventory, 150, t1)
}

func TestSingleReadingSetsBaseline(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	mustAdd(t, b, PointFeed, 0, 100)
	view, _ := b.Latest()
	if view.Current.TotalInput != 0 {
		t.Fatalf("first reading must set baseline, got input=%v", view.Current.TotalInput)
	}
}

func TestBalanceVerdictBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		tolerance float64
		want      string
	}{
		{"loss equals tolerance is balanced", 20, VerdictBalanced},
		{"loss beyond tolerance is loss", 1, VerdictLoss},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewWithTolerance(tc.tolerance)
			mustOpen(t, b, 100, "primary")
			fillBalancedWindow(t, b, 100, 200)
			_, bal, ok := b.CloseWindow(300)
			if !ok {
				t.Fatal("close window returned no result")
			}
			if bal.Loss != 20 {
				t.Fatalf("loss=%v want 20", bal.Loss)
			}
			if bal.Verdict != tc.want {
				t.Fatalf("verdict=%s want %s", bal.Verdict, tc.want)
			}
		})
	}
}

func TestGainVerdict(t *testing.T) {
	b := NewWithTolerance(1)
	mustOpen(t, b, 100, "primary")
	// 投入 100，塔顶产出 60，塔底 0，存量 +45 → 损失 -5 → 盈余。
	mustAdd(t, b, PointFeed, 0, 100)
	mustAdd(t, b, PointOverhead, 0, 100)
	mustAdd(t, b, PointBottoms, 0, 100)
	mustAdd(t, b, PointInventory, 100, 100)
	mustAdd(t, b, PointFeed, 100, 200)
	mustAdd(t, b, PointOverhead, 60, 200)
	mustAdd(t, b, PointBottoms, 0, 200)
	mustAdd(t, b, PointInventory, 145, 200)
	_, bal, _ := b.CloseWindow(300)
	if bal.Loss != -5 {
		t.Fatalf("loss=%v want -5", bal.Loss)
	}
	if bal.Verdict != VerdictGain {
		t.Fatalf("verdict=%s want gain", bal.Verdict)
	}
}

func TestMassBalanceIdentity(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	// 投入 100、产出 60、净积累 40 → 损失 0。
	mustAdd(t, b, PointFeed, 0, 100)
	mustAdd(t, b, PointOverhead, 0, 100)
	mustAdd(t, b, PointBottoms, 0, 100)
	mustAdd(t, b, PointInventory, 100, 100)
	mustAdd(t, b, PointFeed, 100, 200)
	mustAdd(t, b, PointOverhead, 60, 200)
	mustAdd(t, b, PointBottoms, 0, 200)
	mustAdd(t, b, PointInventory, 140, 200)
	_, bal, _ := b.CloseWindow(300)
	if bal.Loss != 0 || bal.Verdict != VerdictBalanced {
		t.Fatalf("loss=%v verdict=%s, want 0/balanced", bal.Loss, bal.Verdict)
	}
}

func TestCumulativeMeterRegression(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	mustAdd(t, b, PointFeed, 100, 100)
	if _, err := b.Add(Reading{Point: PointFeed, Value: 90, At: 200}); !errors.Is(err, ErrMeterWentBackwards) {
		t.Fatalf("forward regression err=%v want ErrMeterWentBackwards", err)
	}
	// 乱序插入一个超过后值的读数同样判倒退（按时间序邻居，而非到达序）。
	if _, err := b.Add(Reading{Point: PointFeed, Value: 100, At: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(Reading{Point: PointFeed, Value: 120, At: 150}); !errors.Is(err, ErrMeterWentBackwards) {
		t.Fatalf("out-of-order regression err=%v want ErrMeterWentBackwards", err)
	}
}

func TestDuplicateTimestampScopedToPoint(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	mustAdd(t, b, PointFeed, 0, 100)
	if _, err := b.Add(Reading{Point: PointFeed, Value: 5, At: 100}); !errors.Is(err, ErrDuplicateTimestamp) {
		t.Fatalf("duplicate err=%v want ErrDuplicateTimestamp", err)
	}
	// 不同计量点共享同一时间戳是允许的。
	mustAdd(t, b, PointOverhead, 0, 100)
}

func TestReadingValidation(t *testing.T) {
	if _, err := ParsePoint("vapor"); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("parse err=%v want ErrUnknownPoint", err)
	}
	b := New()
	mustOpen(t, b, 100, "primary")
	cases := []struct {
		name string
		r    Reading
		want error
	}{
		{"unknown point", Reading{Point: Point("x"), Value: 1, At: 100}, ErrUnknownPoint},
		{"negative value", Reading{Point: PointFeed, Value: -1, At: 100}, ErrNegativeValue},
		{"zero timestamp", Reading{Point: PointFeed, Value: 1, At: 0}, ErrBadTimestamp},
		{"nan value", Reading{Point: PointFeed, Value: math.NaN(), At: 100}, ErrNegativeValue},
	}
	for _, tc := range cases {
		if _, err := b.Add(tc.r); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v want %v", tc.name, err, tc.want)
		}
	}
}

func TestAddBeforeFirstWindow(t *testing.T) {
	b := New()
	if _, err := b.Add(Reading{Point: PointFeed, Value: 1, At: 100}); !errors.Is(err, ErrBeforeFirstWindow) {
		t.Fatalf("err=%v want ErrBeforeFirstWindow", err)
	}
}

func TestSplitCarriesInventory(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	mustAdd(t, b, PointInventory, 150, 200)
	if _, _, ok := b.CloseWindow(300); !ok {
		t.Fatal("close window 1 failed")
	}
	mustOpen(t, b, 300, "secondary")
	view, _ := b.Window(2)
	if !view.Current.InventoryKnown || view.Current.StartInventory != 150 || view.Current.EndInventory != 150 {
		t.Fatalf("carry not applied: known=%v start=%v end=%v", view.Current.InventoryKnown, view.Current.StartInventory, view.Current.EndInventory)
	}
}

func TestSettleFreezesOriginalAndLateReadingRevises(t *testing.T) {
	b := NewWithTolerance(1)
	mustOpen(t, b, 100, "primary")
	// 首条进料表读数从 150 开始；迟到读数 80@120 会成为新的时间序首值，投入由 1000 变 1020。
	mustAdd(t, b, PointFeed, 100, 150)
	mustAdd(t, b, PointOverhead, 0, 150)
	mustAdd(t, b, PointBottoms, 0, 150)
	mustAdd(t, b, PointInventory, 120, 150)
	mustAdd(t, b, PointFeed, 1100, 200)
	mustAdd(t, b, PointOverhead, 700, 200)
	mustAdd(t, b, PointBottoms, 250, 200)
	mustAdd(t, b, PointInventory, 150, 200)
	_, frozen, _ := b.CloseWindow(300)
	if frozen.Loss != 20 || frozen.Verdict != VerdictLoss {
		t.Fatalf("frozen loss=%v verdict=%s, want 20/loss", frozen.Loss, frozen.Verdict)
	}

	ack := mustAdd(t, b, PointFeed, 80, 120) // 落在已结算窗口内的迟到读数
	if !ack.Revised || !ack.Settled || ack.Sequence != 1 {
		t.Fatalf("ack=%+v want revised/settled seq=1", ack)
	}
	view, _ := b.Window(1)
	if view.Original == nil || view.Original.Loss != 20 || view.Original.Verdict != VerdictLoss {
		t.Fatalf("original not frozen: %+v", view.Original)
	}
	if view.Current.Loss != 40 {
		t.Fatalf("current loss=%v want 40", view.Current.Loss)
	}
	if len(view.Revisions) != 1 || view.Revisions[0].Reason != ReasonLateReading {
		t.Fatalf("revisions=%+v want one late-reading revision", view.Revisions)
	}
	if view.Revisions[0].Original.Verdict != VerdictLoss || view.Revisions[0].Revised.Loss != 40 {
		t.Fatal("revision must carry frozen original and recomputed revised")
	}
}

func TestMultipleLateReadingsKeepOriginalFrozen(t *testing.T) {
	b := NewWithTolerance(1)
	mustOpen(t, b, 100, "primary")
	// 进料基线从 100 起，使两条更早的迟到读数 80@120、90@130 在时间序上单调合法。
	mustAdd(t, b, PointFeed, 100, 150)
	mustAdd(t, b, PointOverhead, 0, 150)
	mustAdd(t, b, PointBottoms, 0, 150)
	mustAdd(t, b, PointInventory, 120, 150)
	mustAdd(t, b, PointFeed, 1100, 200)
	mustAdd(t, b, PointOverhead, 700, 200)
	mustAdd(t, b, PointBottoms, 250, 200)
	mustAdd(t, b, PointInventory, 150, 200)
	_, frozen, _ := b.CloseWindow(300)

	mustAdd(t, b, PointFeed, 80, 120)
	mustAdd(t, b, PointFeed, 90, 130)

	view, _ := b.Window(1)
	if len(view.Revisions) != 2 {
		t.Fatalf("revisions=%d want 2", len(view.Revisions))
	}
	if !reflect.DeepEqual(*view.Original, frozen) {
		t.Fatalf("original drifted after revisions: %+v vs %+v", *view.Original, frozen)
	}
	if !reflect.DeepEqual(view.Revisions[0].Original, view.Revisions[1].Original) {
		t.Fatal("every revision must retain the same frozen original")
	}
}

func TestMissingClosingInventoryCompletesViaLateReading(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	// 三个累积点各报一次（差为 0），但窗内没有任何存量读数。
	mustAdd(t, b, PointFeed, 0, 150)
	mustAdd(t, b, PointOverhead, 0, 150)
	mustAdd(t, b, PointBottoms, 0, 150)
	_, bal, _ := b.CloseWindow(300)
	if bal.Complete || bal.Verdict != VerdictIncomplete {
		t.Fatalf("bal=%+v want incomplete", bal)
	}
	view, _ := b.Window(1)
	if !view.Provisional {
		t.Fatal("settled window without inventory reading should be provisional")
	}
	// 迟到存量读数补全窗口，应触发修订并给出真实结论。
	mustAdd(t, b, PointInventory, 140, 200)
	view, _ = b.Window(1)
	if view.Provisional || !view.Current.Complete || view.Current.Verdict != VerdictBalanced {
		t.Fatalf("after late inventory: provisional=%v current=%+v", view.Provisional, view.Current)
	}
	if len(view.Revisions) != 1 || view.Revisions[0].Original.Complete {
		t.Fatal("revision original must remain the incomplete frozen balance")
	}
}

func TestMissingCumulativePointsListed(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	mustAdd(t, b, PointFeed, 0, 100)
	mustAdd(t, b, PointFeed, 100, 200)
	mustAdd(t, b, PointInventory, 100, 100)
	mustAdd(t, b, PointInventory, 100, 200)
	_, bal, _ := b.CloseWindow(300)
	want := []Point{PointOverhead, PointBottoms}
	if !reflect.DeepEqual(bal.MissingPoints, want) {
		t.Fatalf("missing=%v want %v", bal.MissingPoints, want)
	}
	if bal.Complete || bal.Verdict != VerdictIncomplete {
		t.Fatal("window with missing meters must be incomplete")
	}
}

func TestReadingAfterFinalWindowRejected(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	fillBalancedWindow(t, b, 100, 200)
	if _, _, ok := b.CloseWindow(300); !ok {
		t.Fatal("close failed")
	}
	// 末窗是停机闭区间：at==closedAt 仍计入，at>closedAt 拒绝。
	if ack := mustAdd(t, b, PointFeed, 1000, 300); !ack.Revised {
		t.Fatal("reading at shutdown instant should revise final window")
	}
	if _, err := b.Add(Reading{Point: PointFeed, Value: 1001, At: 301}); !errors.Is(err, ErrAfterLastWindow) {
		t.Fatalf("err=%v want ErrAfterLastWindow", err)
	}
}

func TestBoundaryTimestampRoutesToSuccessor(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	mustAdd(t, b, PointFeed, 0, 150)
	mustAdd(t, b, PointFeed, 100, 200)
	if _, _, ok := b.CloseWindow(300); !ok {
		t.Fatal("close w1 failed")
	}
	mustOpen(t, b, 300, "secondary")

	// at==closedAt 按半开约定归后继窗口；at<closedAt 归前一窗。
	ackOld := mustAdd(t, b, PointFeed, 100, 299)
	if ackOld.Sequence != 1 || !ackOld.Revised {
		t.Fatalf("299 routed to seq=%d revised=%v, want 1/true", ackOld.Sequence, ackOld.Revised)
	}
	ackNew := mustAdd(t, b, PointFeed, 50, 300)
	if ackNew.Sequence != 2 || ackNew.Revised {
		t.Fatalf("300 routed to seq=%d revised=%v, want 2/false", ackNew.Sequence, ackNew.Revised)
	}
}

func TestCloseWindowIdempotent(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	if _, _, ok := b.CloseWindow(300); !ok {
		t.Fatal("first close should settle")
	}
	if _, _, ok := b.CloseWindow(300); ok {
		t.Fatal("second close must be a no-op")
	}
	if len(b.Windows()) != 1 {
		t.Fatalf("windows=%d want 1", len(b.Windows()))
	}
}

func TestOpenWhileOpenAndConfigure(t *testing.T) {
	b := NewWithTolerance(5)
	mustOpen(t, b, 100, "primary")
	if _, err := b.OpenWindow(200, "secondary", uuid.New(), model.NewCampaign("g")); !errors.Is(err, ErrWindowAlreadyOpen) {
		t.Fatalf("err=%v want ErrWindowAlreadyOpen", err)
	}
	if _, err := b.OpenWindow(200, "", uuid.New(), model.NewCampaign("g")); !errors.Is(err, ErrSourceRequired) {
		t.Fatalf("err=%v want ErrSourceRequired", err)
	}
	b.Configure(-1)
	if b.Tolerance() != 5 {
		t.Fatalf("negative tolerance applied: %v", b.Tolerance())
	}
	b.Configure(2)
	if b.Tolerance() != 2 {
		t.Fatalf("tolerance=%v want 2", b.Tolerance())
	}
}

func TestSourceSwitchesCreateSequentialWindows(t *testing.T) {
	b := New()
	revs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	c := model.NewCampaign("g")
	for i, source := range []string{"primary", "secondary", "tertiary"} {
		at := int64(100 + i*200)
		if i > 0 {
			if _, _, ok := b.CloseWindow(at); !ok {
				t.Fatalf("close before %s failed", source)
			}
		}
		if _, err := b.OpenWindow(at, source, revs[i], c); err != nil {
			t.Fatalf("open %s: %v", source, err)
		}
	}
	windows := b.Windows()
	if len(windows) != 3 {
		t.Fatalf("windows=%d want 3", len(windows))
	}
	for i, source := range []string{"primary", "secondary", "tertiary"} {
		if windows[i].Meta.Sequence != i+1 || windows[i].Meta.Source != source || windows[i].Meta.FeedRevision != revs[i] {
			t.Fatalf("window %d meta=%+v", i+1, windows[i].Meta)
		}
	}
}

func TestOutOfOrderArrivalComputedByTimestamp(t *testing.T) {
	b := New()
	mustOpen(t, b, 100, "primary")
	// 到达顺序 300 → 100 → 200，衡算必须按时间序取首尾差 1000。
	mustAdd(t, b, PointFeed, 1000, 300)
	mustAdd(t, b, PointFeed, 0, 100)
	mustAdd(t, b, PointFeed, 500, 200)
	view, _ := b.Latest()
	if view.Current.TotalInput != 1000 {
		t.Fatalf("input=%v want 1000 (timestamp-ordered)", view.Current.TotalInput)
	}
}

func TestSummaryShape(t *testing.T) {
	b := New()
	if _, ok := b.Latest(); ok {
		t.Fatal("empty book should have no latest window")
	}
	mustOpen(t, b, 100, "primary")
	fillBalancedWindow(t, b, 100, 200)
	b.CloseWindow(300)
	mustOpen(t, b, 300, "secondary")
	mustAdd(t, b, PointFeed, 20, 350) // 落在打开的窗口 2（首条为基线），不产生修订
	summary := b.Summary()
	if summary["windows"] != 2 || summary["settled"] != 1 || summary["open"] != true || summary["revisions"] != 0 {
		t.Fatalf("summary=%v", summary)
	}
	if _, ok := summary["latest"].(WindowView); !ok {
		t.Fatal("latest should be a WindowView")
	}
}
