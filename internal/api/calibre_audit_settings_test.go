package api

import "testing"

func TestCWAWebURLValidation(t *testing.T) {
	for _, value := range []string{"", "http://localhost:8083", "https://cwa.example.org/library/"} {
		if err := validateSettingValue(SettingCWAWebURL, value); err != nil {
			t.Errorf("accepted %q: %v", value, err)
		}
	}
	for _, value := range []string{"javascript:alert(1)", "/relative", "https://user:pass@cwa.example.org", "https://cwa.example.org/?token=x"} {
		if err := validateSettingValue(SettingCWAWebURL, value); err == nil {
			t.Errorf("accepted unsafe URL %q", value)
		}
	}
}
