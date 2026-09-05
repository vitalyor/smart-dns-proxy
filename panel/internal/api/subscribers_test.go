package api

import (
	"encoding/json"
	"testing"
)

// A PATCH that mentions only the name must leave every other field alone. The
// previous struct could not express that: an absent limit looked exactly like
// "сбросить", so renaming a user silently removed their limits.
func TestSubscriberPatchDistinguishesAbsentFromNull(t *testing.T) {
	var only subscriberRequest
	if err := json.Unmarshal([]byte(`{"name":"Иван"}`), &only); err != nil {
		t.Fatal(err)
	}
	if only.DeviceLimit.Set || only.QueryLimit.Set {
		t.Fatal("отсутствующее поле не должно считаться заданным")
	}
	if only.Note != nil || only.ExpiresAt != nil {
		t.Fatal("отсутствующие note/expires_at не должны трогаться")
	}

	var cleared subscriberRequest
	if err := json.Unmarshal([]byte(`{"device_limit":null,"query_limit":null}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared.DeviceLimit.Set || cleared.DeviceLimit.Value != nil {
		t.Fatal("явный null — это «снять лимит», а не «не трогать»")
	}
	if !cleared.QueryLimit.Set || cleared.QueryLimit.Value != nil {
		t.Fatal("явный null для query_limit потерян")
	}

	var set subscriberRequest
	if err := json.Unmarshal([]byte(`{"device_limit":5,"query_limit":900000,"note":""}`), &set); err != nil {
		t.Fatal(err)
	}
	if !set.DeviceLimit.Set || set.DeviceLimit.Value == nil || *set.DeviceLimit.Value != 5 {
		t.Fatalf("device_limit = %+v, ожидалось 5", set.DeviceLimit)
	}
	if !set.QueryLimit.Set || set.QueryLimit.Value == nil || *set.QueryLimit.Value != 900000 {
		t.Fatalf("query_limit = %+v, ожидалось 900000", set.QueryLimit)
	}
	// Пустая строка — это осознанная очистка заметки, а не её отсутствие.
	if set.Note == nil || *set.Note != "" {
		t.Fatal("пустая note должна доезжать как значение")
	}
}
