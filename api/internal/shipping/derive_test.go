package shipping

import "testing"

// The derivation of orders.fulfillment_status (change partial-shipment, design
// D2) is a pure function precisely so that it can be pinned down here, with no
// database and therefore inside `make test` rather than only `make test-int`.
//
// The first block of cases is the one that matters most: **it is the behavior
// this system had before line items existed**. A single shipment covering the
// whole order used to drive the order 0 → 1 → 2 (or → 3), and those transitions
// were the entire contract every existing consumer was written against. They are
// not "still roughly right" — they are branches of the new function, and if any
// of them moves, an existing storefront starts lying.
func TestDeriveFulfillmentStatusReproducesSingleShipmentHistory(t *testing.T) {
	const ordered = 3

	cases := []struct {
		name     string
		statuses []int16
		shipped  int32
		want     int16
	}{
		{"沒有出貨紀錄＝未出貨", nil, 0, 0},
		{"一筆涵蓋全訂單的出貨＝已出貨", []int16{ShippedStatus}, ordered, ShippedStatus},
		{"該筆出貨送達＝已送達", []int16{DeliveredStatus}, ordered, DeliveredStatus},
		{"該筆出貨退回＝已退貨", []int16{ReturnedStatus}, ordered, ReturnedStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveFulfillmentStatus(tc.statuses, ordered, tc.shipped); got != tc.want {
				t.Fatalf("derive(%v, ordered=%d, shipped=%d) = %d, want %d (這是 change "+
					"partial-shipment 之前就存在的行為，改動它會讓既有前台顯示錯誤)",
					tc.statuses, ordered, tc.shipped, got, tc.want)
			}
		})
	}
}

func TestDeriveFulfillmentStatusMultipleShipments(t *testing.T) {
	const ordered = 3

	cases := []struct {
		name     string
		statuses []int16
		shipped  int32
		want     int16
	}{
		{
			name:     "出了一部分＝部分出貨",
			statuses: []int16{ShippedStatus}, shipped: 1, want: PartiallyShippedStatus,
		},
		{
			// 分支順序的整個重點：已送達的那一箱不能讓一張還沒出完的訂單看起來已完成。
			name:     "出了一部分且那一箱已送達，仍是部分出貨",
			statuses: []int16{DeliveredStatus}, shipped: 2, want: PartiallyShippedStatus,
		},
		{
			name:     "兩箱出完、其一送達＝已出貨",
			statuses: []int16{DeliveredStatus, ShippedStatus}, shipped: ordered, want: ShippedStatus,
		},
		{
			name:     "兩箱出完且皆送達＝已送達",
			statuses: []int16{DeliveredStatus, DeliveredStatus}, shipped: ordered, want: DeliveredStatus,
		},
		{
			// 退貨壓過一切，包含「還沒出完」。訂單層沒有「部分退貨」的詞彙，而點數扣回
			// 本來就是整筆訂單的全有全無（member-tiers-and-points）。
			name:     "任一箱退回＝已退貨，即使還沒出完",
			statuses: []int16{ReturnedStatus, ShippedStatus}, shipped: 2, want: ReturnedStatus,
		},
		{
			name:     "任一箱退回＝已退貨，即使其餘皆已送達",
			statuses: []int16{DeliveredStatus, ReturnedStatus}, shipped: ordered, want: ReturnedStatus,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveFulfillmentStatus(tc.statuses, ordered, tc.shipped); got != tc.want {
				t.Fatalf("derive(%v, ordered=%d, shipped=%d) = %d, want %d",
					tc.statuses, ordered, tc.shipped, got, tc.want)
			}
		})
	}
}

// A shipment row never carries the partially-shipped value, and neither does the
// derivation ever produce it out of thin air: it can only come from the
// quantity comparison. Stated as a test because shipments_status_check (1,2,3)
// enforces the storage side but nothing enforces the arithmetic side.
func TestDeriveFulfillmentStatusNeverInventsPartialWithoutMissingQuantity(t *testing.T) {
	for _, st := range [][]int16{
		{ShippedStatus}, {DeliveredStatus}, {ShippedStatus, DeliveredStatus},
	} {
		if got := deriveFulfillmentStatus(st, 5, 5); got == PartiallyShippedStatus {
			t.Fatalf("derive(%v, 5, 5) = 部分出貨，但每一件都出了", st)
		}
	}
}
