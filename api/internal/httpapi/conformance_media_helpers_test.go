package httpapi

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"testing"
)

// conformanceJPEG is a w x h gradient JPEG for the media flow.
func conformanceJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 90, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// conformanceUpload runs presign -> browser-style PUT with the returned headers
// and returns the upload id; the presign response is contract-validated.
func conformanceUpload(t *testing.T, e *conformanceEnv, v *contractValidator, adminTok, shopPrefix, purpose string, data []byte) string {
	t.Helper()
	code, body := e.do(t, http.MethodPost, shopPrefix+"/media/uploads", adminTok, map[string]any{
		"content_type": "image/jpeg", "size_bytes": len(data), "purpose": purpose,
	})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/admin/shops/{shopID}/media/uploads", http.MethodPost, http.StatusCreated, body)
	var up struct {
		UploadID  string            `json:"upload_id"`
		UploadURL string            `json:"upload_url"`
		Headers   map[string]string `json:"headers"`
	}
	decodeInto(t, body, &up)
	req, _ := http.NewRequest(http.MethodPut, up.UploadURL, bytes.NewReader(data))
	for k, hv := range up.Headers {
		req.Header.Set(k, hv)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT to presigned URL: err=%v resp=%v", err, resp)
	}
	resp.Body.Close()
	return up.UploadID
}
