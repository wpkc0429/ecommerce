// Package ecpay implements 綠界科技 ECPay 全方位金流（AIO）as a
// payment.Provider (change ecpay-payment-provider).
//
// **形狀是真的，端點是可注入的。** Every field name, the CheckMacValue algorithm
// and the `1|OK` acknowledgement are exactly what 綠界's public documentation
// specifies; the only thing configuration changes is *which host* receives the
// form. That is what makes "先做模擬流程" mean something other than "假裝送出":
// the bytes this package produces are byte-identical whether the cashier URL
// points at api/cmd/ecpay-sim or at payment.ecpay.com.tw.
//
// This package deliberately lives outside internal/payment: everything 綠界-
// specific — field names, RtnCode values, the .NET URL-encoding table — is
// vocabulary that must never leak into the provider-neutral layer (design D1 of
// change payment-integration).
//
// 文件來源（以中文正文為準；文件頁尾的 YAML 區塊與正文有已知不一致，見 design
// Risks）：
//
//	產生訂單            https://developers.ecpay.com.tw/2862/
//	檢查碼機制說明      https://developers.ecpay.com.tw/2902/
//	URLEncode 轉換表    https://developers.ecpay.com.tw/2904/
//	付款結果通知        https://developers.ecpay.com.tw/2878/
//	測試介接資訊        https://developers.ecpay.com.tw/2856/
package ecpay

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

// FieldCheckMacValue is the name of the checksum field itself — excluded from
// its own computation.
const FieldCheckMacValue = "CheckMacValue"

// dotNetUnescape restores the characters .NET's HttpUtility.UrlEncode leaves
// alone but Go's url.QueryEscape percent-encodes.
//
// 來源：綠界「URLEncode 轉換表」(/2904/)。官方 PHP SDK 的順序是
// urlencode() → strtolower() → 這七個 str_replace，本實作照抄該順序。
var dotNetUnescape = strings.NewReplacer(
	"%2d", "-",
	"%5f", "_",
	"%2e", ".",
	"%21", "!",
	"%2a", "*",
	"%28", "(",
	"%29", ")",
)

// CheckMacValue computes 綠界's checksum over params, excluding any existing
// CheckMacValue entry.
//
// The algorithm, verbatim from /2902/:
//
//  1. 除 CheckMacValue 外所有參數，依參數名 A→Z 排序，以 & 串接 name=value
//  2. 最前面加 HashKey=<key>& ，最後面加 &HashIV=<iv>
//  3. 整串 URL encode（須符合「.NET 編碼」）
//  4. 轉小寫
//  5. SHA256
//  6. 轉大寫
//
// **三個地方寫錯不會噴錯，只會得到一個看起來很正常的錯誤簽章：**
//
//   - **排序是大小寫不敏感的.** 權威來源是官方 PHP SDK 的 ArrayService::naturalSort，
//     它用的是 uksort + strcasecmp。這件事在 NeedExtraPaidInfo=Y 時會咬人——綠界
//     回傳的額外參數裡有小寫欄位（gwsr、auth_code、process_date、amount…），
//     ASCII 序會把它們全部排到大寫欄位之後，答案就錯了。
//
//   - **`~` 必須維持編碼為 %7e，而 Go 的 url.QueryEscape 不編碼它.** 這是轉換表上
//     唯一一個「.NET 編碼比標準編碼更積極」的字元，所有講這個演算法的文章都只在
//     講反方向（把 %2d 還原成 -）。PHP 沒有這個問題，這是 Go 專屬的地雷。
//
//   - **順序必須是 encode → 轉小寫 → 替換.** 顛倒過來，先替換再轉小寫會把補上的
//     %7E 一起轉成 %7e（碰巧仍然對），但先轉小寫再 encode 會把整個值弄壞。
//
// 唯一能證明這三件事都做對的，是綠界文件上的官方範例向量——見 checkmac_test.go。
func CheckMacValue(hashKey, hashIV string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == FieldCheckMacValue {
			continue
		}
		keys = append(keys, k)
	}
	// Step 1: case-insensitive A→Z. strings.EqualFold-style comparison via
	// ToLower, with the raw name as a stable tiebreaker so the result never
	// depends on map iteration order.
	sort.Slice(keys, func(i, j int) bool {
		li, lj := strings.ToLower(keys[i]), strings.ToLower(keys[j])
		if li != lj {
			return li < lj
		}
		return keys[i] < keys[j]
	})

	var b strings.Builder
	// Step 2: HashKey前綴.
	b.WriteString("HashKey=")
	b.WriteString(hashKey)
	for _, k := range keys {
		b.WriteString("&")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params[k])
	}
	// Step 2: HashIV後綴.
	b.WriteString("&HashIV=")
	b.WriteString(hashIV)

	// Step 3/4/5/6.
	sum := sha256.Sum256([]byte(dotNetURLEncode(b.String())))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// dotNetURLEncode performs steps 3 and 4: encode the whole string the way .NET's
// HttpUtility.UrlEncode does, then lowercase it.
func dotNetURLEncode(s string) string {
	// url.QueryEscape already matches .NET for the cases that matter most:
	// space → '+', and UTF-8 bytes → uppercase %XX (which step 4 lowercases).
	encoded := strings.ToLower(url.QueryEscape(s))
	// Go leaves '~' alone; 綠界 requires it encoded. Applied BEFORE the
	// unescape table so the literal stays lowercase like everything else.
	encoded = strings.ReplaceAll(encoded, "~", "%7e")
	return dotNetUnescape.Replace(encoded)
}

// Sign computes the checksum for params and returns a copy of params carrying
// it under FieldCheckMacValue. The input map is never mutated.
func Sign(hashKey, hashIV string, params map[string]string) map[string]string {
	out := make(map[string]string, len(params)+1)
	for k, v := range params {
		if k == FieldCheckMacValue {
			continue
		}
		out[k] = v
	}
	out[FieldCheckMacValue] = CheckMacValue(hashKey, hashIV, out)
	return out
}

// SignCallback produces the form-urlencoded body of a payment result
// notification that this package's VerifyWebhook will accept.
//
// Exported for exactly the reason payment.SignMockWebhook is (and with the same
// caveat): tests and api/cmd/ecpay-sim need to construct a *valid* 綠界 callback
// without re-implementing the checksum, because a second implementation would
// eventually disagree with this one and the test would then be verifying itself.
// It is a test/simulator helper, not a production path — nothing in the server
// calls it.
func SignCallback(hashKey, hashIV string, fields map[string]string) string {
	signed := Sign(hashKey, hashIV, fields)
	values := url.Values{}
	for k, v := range signed {
		values.Set(k, v)
	}
	return values.Encode()
}
