// Command ecpay-sim is a local stand-in for 綠界's cashier (change
// ecpay-payment-provider, design D11).
//
// It exists so the FULL payment flow — build the form, POST it to a cashier,
// have that cashier send a payment result notification back, and answer it with
// `1|OK` — can be walked through by a human with **no 綠界 account and no
// outbound network**, while every byte on the wire is 綠界's real shape.
//
// ── Why this is a separate binary and not a route on the API ────────────
//
//  1. **It would be locked into v1.** The contract's route-coverage check
//     requires every registered route to appear in openapi.yaml, and
//     v1-surface.txt records every documented route — including the non-/api/v1
//     GET /healthz. Mounting a simulator would make it a promise that "deleting
//     is breaking v1"… for a thing whose whole purpose is to be deleted the day
//     the real 綠界 integration lands. Putting a scheduled deletion inside a
//     mechanism whose entire meaning is "nothing here gets deleted" teaches the
//     next person to ignore the mechanism.
//  2. **Config-dependent routing is a landmine.** Mounting it behind a flag
//     makes production's route table differ from the one the contract checks
//     walk; mounting it always and 404ing makes production carry a permanent
//     fake route.
//  3. **A separate process is what actually makes it a simulation.** 綠界 is
//     another machine: it receives our form over HTTP and calls our ReturnURL
//     over HTTP. An in-process handler would skip serialization, absolute
//     cross-host URLs, and the "ReturnURL must be reachable from the outside"
//     constraint — the three things that actually break in the real world.
//
// **Automated tests do not need this binary.** Go integration tests and
// e2e/commerce-path.mjs sign their own callbacks, exactly as they already do for
// the mock provider's HMAC. This is for humans.
//
// Usage:
//
//	make ecpay-sim            # :8090, using the same defaults as the API
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/payment/ecpay"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	s := &simulator{
		hashKey: getenv("ECPAY_HASH_KEY", "pwFHCqoQZGmho4w6"),
		hashIV:  getenv("ECPAY_HASH_IV", "EkRm7iFT261dpevs"),
	}

	mux := http.NewServeMux()
	// 綠界's real path, deliberately.
	mux.HandleFunc("/Cashier/AioCheckOut/V5", s.cashier)
	mux.HandleFunc("/pay", s.pay)

	log.Printf("ecpay-sim listening on %s", *addr)
	log.Printf("point ECPAY_CHECKOUT_URL at http://localhost%s/Cashier/AioCheckOut/V5", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

type simulator struct {
	hashKey string
	hashIV  string
}

// cashier receives the AioCheckOut form and independently re-verifies the
// checksum the API produced.
//
// **This verification is the part with real value.** It plays the receiving side
// of the algorithm against the actual bytes that were submitted through a
// browser's form encoding — which is a different path from the unit test's
// in-memory map, and the place a wrong Content-Type or a mangled ItemName would
// show up.
func (s *simulator) cashier(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// 綠界's cashier is POST-only, and saying so out loud is half the point
		// of this simulator existing.
		http.Error(w, "綠界 AioCheckOut is POST-only (this is why redirect_url cannot carry it)", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	fields := map[string]string{}
	for k := range r.PostForm {
		fields[k] = r.PostForm.Get(k)
	}
	got := fields[ecpay.FieldCheckMacValue]
	want := ecpay.CheckMacValue(s.hashKey, s.hashIV, fields)
	if !strings.EqualFold(got, want) {
		renderPage(w, http.StatusBadRequest, pageData{
			Title: "CheckMacValue 不符 — 綠界會拒絕這筆交易",
			Error: fmt.Sprintf("收到 %s\n重算 %s", got, want),
			Rows:  sortedRows(fields),
		})
		return
	}

	renderPage(w, http.StatusOK, pageData{
		Title:      "模擬綠界收銀台（CheckMacValue 驗證通過）",
		Rows:       sortedRows(fields),
		ShowPay:    true,
		ReturnURL:  fields["ReturnURL"],
		BackURL:    fields["ClientBackURL"],
		MerchantID: fields["MerchantID"],
		TradeNo:    fields["MerchantTradeNo"],
		Amount:     fields["TotalAmount"],
	})
}

// payOutcome names the three buttons the cashier page offers. They map one to
// one onto payment.Outcome's three values, and the third one exists precisely
// because it is the one nobody expects.
type payOutcome struct {
	rtnCode      string
	rtnMsg       string
	simulatePaid string
}

var payOutcomes = map[string]payOutcome{
	"success": {rtnCode: "1", rtnMsg: "交易成功", simulatePaid: "0"},
	"fail":    {rtnCode: "10100252", rtnMsg: "額度不足", simulatePaid: "0"},
	// 綠界's back-office "模擬付款" button: RtnCode=1, RtnMsg=交易成功, and no
	// money settled. A system that reads only RtnCode marks the order paid,
	// awards loyalty points and queues it for shipping — with nothing in the
	// bank. It is a button here so that hazard is something you can press.
	"simulate": {rtnCode: "1", rtnMsg: "交易成功", simulatePaid: "1"},
}

// pay sends the payment result notification to the merchant's ReturnURL, exactly
// as 綠界 does: form-urlencoded, server-to-server, signed — and it checks that
// the answer is the literal `1|OK`.
func (s *simulator) pay(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	outcome, ok := payOutcomes[r.PostForm.Get("outcome")]
	if !ok {
		http.Error(w, "unknown outcome", http.StatusBadRequest)
		return
	}
	returnURL := r.PostForm.Get("ReturnURL")
	now := time.Now()

	fields := map[string]string{
		"MerchantID":      r.PostForm.Get("MerchantID"),
		"MerchantTradeNo": r.PostForm.Get("MerchantTradeNo"),
		"StoreID":         "",
		"RtnCode":         outcome.rtnCode,
		"RtnMsg":          outcome.rtnMsg,
		// 綠界's own transaction id: 16 digits, yyMMddHHmmss + serial.
		"TradeNo":              now.Format("0601021504") + strconv.Itoa(int(now.Unix()%1000000)),
		"TradeAmt":             r.PostForm.Get("TotalAmount"),
		"PaymentDate":          now.Format("2006/01/02 15:04:05"),
		"PaymentType":          "Credit_CreditCard",
		"PaymentTypeChargeFee": "10",
		"TradeDate":            now.Format("2006/01/02 15:04:05"),
		"SimulatePaid":         outcome.simulatePaid,
		"CustomField1":         "",
		"CustomField2":         "",
		"CustomField3":         "",
		"CustomField4":         "",
	}
	body := ecpay.SignCallback(s.hashKey, s.hashIV, fields)

	status, reply, err := postNotification(returnURL, body)
	data := pageData{
		Title:     "已送出付款結果通知",
		Rows:      sortedRows(fields),
		BackURL:   r.PostForm.Get("ClientBackURL"),
		AckStatus: status,
		AckBody:   reply,
	}
	switch {
	case err != nil:
		data.Error = "無法送達 ReturnURL：" + err.Error()
	case reply != "1|OK":
		// 綠界's documented behaviour: anything other than the literal 1|OK is a
		// failed delivery, redelivered every 5–15 minutes, up to 4 times a day.
		data.Error = fmt.Sprintf("商店回應不是 %q —— 綠界會判定通知失敗並每 5~15 分鐘重送，當天最多 4 次。", "1|OK")
	default:
		data.AckOK = true
	}
	renderPage(w, http.StatusOK, data)
}

func postNotification(returnURL, body string) (int, string, error) {
	if returnURL == "" {
		return 0, "", errors.New("the form carried no ReturnURL")
	}
	if _, err := url.Parse(returnURL); err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, returnURL, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, strings.TrimSpace(string(raw)), nil
}

// ── rendering ──────────────────────────────────────────────────────────

type row struct{ Name, Value string }

type pageData struct {
	Title      string
	Error      string
	Rows       []row
	ShowPay    bool
	ReturnURL  string
	BackURL    string
	MerchantID string
	TradeNo    string
	Amount     string
	AckStatus  int
	AckBody    string
	AckOK      bool
}

func sortedRows(fields map[string]string) []row {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	// Same case-insensitive order the checksum uses, so the page reads like the
	// string that was hashed.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && strings.ToLower(names[j]) < strings.ToLower(names[j-1]); j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	out := make([]row, 0, len(names))
	for _, n := range names {
		out = append(out, row{Name: n, Value: fields[n]})
	}
	return out
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="zh-Hant"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
 body{font:14px/1.6 system-ui,sans-serif;max-width:820px;margin:2rem auto;padding:0 1rem}
 h1{font-size:1.2rem} table{border-collapse:collapse;width:100%;margin:1rem 0}
 td{border-bottom:1px solid #ddd;padding:.35rem .5rem;vertical-align:top}
 td:first-child{width:14rem;color:#555;font-family:ui-monospace,monospace}
 .err{background:#fee;border:1px solid #c66;padding:.75rem;white-space:pre-wrap}
 .ok{background:#efe;border:1px solid #6a6;padding:.75rem}
 button{font:inherit;padding:.5rem .9rem;margin-right:.5rem;cursor:pointer}
 .note{color:#666;font-size:.9em}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .AckOK}}<p class="ok">商店回應 <code>1|OK</code>（HTTP {{.AckStatus}}）—— 綠界會視為通知成功，不再重送。</p>{{end}}
{{if .ShowPay}}
<form method="post" action="/pay">
  <input type="hidden" name="ReturnURL" value="{{.ReturnURL}}">
  <input type="hidden" name="ClientBackURL" value="{{.BackURL}}">
  <input type="hidden" name="MerchantID" value="{{.MerchantID}}">
  <input type="hidden" name="MerchantTradeNo" value="{{.TradeNo}}">
  <input type="hidden" name="TotalAmount" value="{{.Amount}}">
  <button name="outcome" value="success">付款成功</button>
  <button name="outcome" value="fail">付款失敗（額度不足）</button>
  <button name="outcome" value="simulate">模擬付款（RtnCode=1，但綠界不撥款）</button>
</form>
<p class="note">第三顆按鈕送出的通知 <code>RtnCode=1</code>、<code>RtnMsg=交易成功</code>、<code>SimulatePaid=1</code>。
只讀 RtnCode 的系統會把訂單標成已付款並核發點數，而帳上一毛錢都沒有。正確的行為是訂單維持未付款。</p>
{{end}}
{{if .AckBody}}<p class="note">商店回應主體：<code>{{.AckBody}}</code>（HTTP {{.AckStatus}}）</p>{{end}}
<table>{{range .Rows}}<tr><td>{{.Name}}</td><td>{{.Value}}</td></tr>{{end}}</table>
{{if .BackURL}}<p><a href="{{.BackURL}}">返回商店</a></p>{{end}}
</body></html>`))

func renderPage(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := page.Execute(w, data); err != nil {
		log.Printf("render: %v", err)
	}
}
