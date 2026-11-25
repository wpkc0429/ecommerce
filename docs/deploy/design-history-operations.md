# 設計歷史：操作員手冊（切片 1）

給負責部署平台的人。設計與契約見 `docs/design-history.md`。

## 1. 套用與授權

```bash
make migrate      # 兩支 migration：…_add_design_history 與 …_add_design_history_append_only_triggers（純新增、可逆）
make seed         # 建立 design.* 與 design_service.manage 權限節點並授予角色
```

**未 `make seed` 前 owner 看不到歷史、super_admin 簽不了憑證。** `/me` 的 `design.*` 是 UI 的可見性來源。
回滾：`make migrate-down` 兩次（先 trigger 再表；表內資料會一併消失，先 `-export-shop`）。

## 2. 為新商家簽發服務憑證（平台層，人工）

只有 `super_admin`（`design_service.manage`）能做，商家不能自助。

```bash
curl -sS -X POST "$API/api/v1/admin/design-service/credentials" \
  -H "Authorization: Bearer $PLATFORM_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"shop_id": 3, "name": "ai-edit-service", "allowed_cidrs": ["203.0.113.0/24"], "expires_in_days": 90}'
```

回應的 `key`（`dsk_…`）**只出現這一次**：直接放進 AI edit service 的祕密管理，不要貼進聊天或工單。
列表與撤銷：`GET …/credentials?shop_id=3`、`POST …/credentials/{id}/revoke`（立即生效）。
**輪替**：`POST …/credentials/{id}/rotate` 簽發新金鑰，舊的保留 7 天後到期；服務應在到期前 14 天提出輪替。
每店一把（爆炸半徑＝一家店）。與商家 repo 的 git token 分開保管。

機器路由族 `/api/v1/design-service/**` **不經 Cloudflare Access**（服務沒有 user 身分）。建議在部署層另加一條規則，
只允許服務的出口 IP 或 Access service token 打這個路徑前綴；並用 `allowed_cidrs` 在程式層再限制一次。

## 3. 排程（repo 沒有排程器）

```bash
# 每 5 分鐘：把卡住的請求標為 failed（先 dry run 看一次）
cd api && go run ./cmd/design-sweep            # dry run
cd api && go run ./cmd/design-sweep -apply
# 每日：驗證事件雜湊串（有任何斷鏈會以 exit 1 結束）
cd api && go run ./cmd/design-sweep -verify
# 每日：保存期限清除（先 dry run 看一次）
cd api && go run ./cmd/purge-design-history
cd api && go run ./cmd/purge-design-history -apply
```

Windows 工作排程器同理。`design-sweep -orphans` 在切片 2 才有意義。

## 4. 保存期限與調整

預設值與環境變數見 `.env.example` 的 “design history” 區塊（`DESIGN_PROMPT_RETENTION`、`DESIGN_AUDIT_RETENTION` 等，皆有上下限，超出範圍拒絕啟動）。
個資請求或商家下架：`-export-shop N` 匯出 JSON（不含簽名網址），確認後 `-all-for-shop N -apply` 刪除（只影響該店；服務憑證不在其中，請另行撤銷）。

## 5. 切片 1 的限制（刻意）

沒有截圖儲存，所以 `submit` 回 503 `design_storage_not_configured`，請求停在 `previewing`；`GET …/screenshots` 恆為 503。
切片 2（私有 R2 bucket、`PresignGet`、上傳端點）上線後本節與 `docs/design-history.md` 一併更新；
那時需要：第二個 R2 bucket（**不綁自訂網域、不開 r2.dev**、token 只限該 bucket）、sandbox 堆疊、手冊的匿名讀取驗證（task 9.3）。
