package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Токен устройства — это доступ к DNS. Он не должен уезжать в браузер ни в
// одной строке лога, а вместо него должно появляться имя устройства.
func TestSwapTokensForNames(t *testing.T) {
	raw := []byte(`{"seq":7,"entries":[
		{"seq":6,"client":"1.2.3.4","token":"A1B2C3D4","name":"gemini.google.com"},
		{"seq":7,"client":"1.2.3.5","token":"unknown1","name":"example.com"},
		{"seq":5,"client":"1.2.3.6","name":"plain.example"}]}`)

	out := swapTokensForNames(raw, map[string]string{"a1b2c3d4": "Ноутбук"})

	for _, leak := range []string{"token", "a1b2c3d4", "unknown1"} {
		if bytes.Contains(bytes.ToLower(out), []byte(leak)) {
			t.Fatalf("токен утёк в ответ панели (%s): %s", leak, out)
		}
	}
	var got struct {
		Entries []struct {
			Seq    int    `json:"seq"`
			Device string `json:"device"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[int]string{6: "Ноутбук", 7: "", 5: ""}
	if len(got.Entries) != 3 {
		t.Fatalf("потеряны строки: %s", out)
	}
	for _, e := range got.Entries {
		if e.Device != want[e.Seq] {
			t.Errorf("строка %d: устройство %q, ожидали %q", e.Seq, e.Device, want[e.Seq])
		}
	}
}
