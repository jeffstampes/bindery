package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// Issue #2: the Calibre/CWA authoritative-library mode is opt-in and its whole
// first slice is configuration. These tests pin the two properties the issue
// calls acceptance criteria — a fresh or upgraded install behaves exactly as
// before unless the key is explicitly set to "true", and the key round-trips
// through the existing settings machinery — plus the one dependency rule the
// setting carries: the mode reads metadata.db out of calibre.library_path, so
// the two cannot be configured into a state where the mode has nothing to
// read.

// isCalibreAuthoritativeLibraryEnabled is how a caller reads the flag: the
// stored string, folded, compared against "true". Nothing in the tree reads it
// yet — this slice is configuration and documentation only — so the rule lives
// here rather than as an exported helper with no callers.
func isCalibreAuthoritativeLibraryEnabled(t *testing.T, repo *db.SettingsRepo) bool {
	t.Helper()
	s, err := repo.Get(context.Background(), SettingCalibreAuthoritativeLibraryEnabled)
	if err != nil {
		t.Fatalf("read %s: %v", SettingCalibreAuthoritativeLibraryEnabled, err)
	}
	if s == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(s.Value), "true")
}

// TestCalibreAuthoritativeLibrary_DefaultsOff is the invariant the rest of the
// feature rests on. Nothing is stored on a fresh install, nothing is stored
// after an upgrade (there is no migration seeding the key), and both read back
// as off.
func TestCalibreAuthoritativeLibrary_DefaultsOff(t *testing.T) {
	_, repo, ctx := settingsFixture(t)

	s, err := repo.Get(ctx, SettingCalibreAuthoritativeLibraryEnabled)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s != nil {
		t.Fatalf("a fresh install already stores %s = %q; the mode must be opt-in", SettingCalibreAuthoritativeLibraryEnabled, s.Value)
	}
	if isCalibreAuthoritativeLibraryEnabled(t, repo) {
		t.Error("authoritative-library mode reads as on with nothing stored")
	}
}

// TestCalibreAuthoritativeLibrary_LeavesExistingCalibreConfigAlone checks the
// "existing behaviour is unchanged" half of the acceptance criteria at the
// place it could actually regress: LoadCalibreConfig is what every Calibre
// flow is built from, so the new key must not move any field in it.
func TestCalibreAuthoritativeLibrary_LeavesExistingCalibreConfigAlone(t *testing.T) {
	_, repo, ctx := settingsFixture(t)
	tmp := t.TempDir()
	for k, v := range map[string]string{
		SettingCalibreLibraryPath:          tmp,
		SettingCalibreMode:                 "calibredb",
		SettingCalibreLibraryImportEnabled: "true",
		SettingCalibreSyncOnStartup:        "true",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	before := LoadCalibreConfig(ctx, repo)

	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	after := LoadCalibreConfig(ctx, repo)

	if !after.AuthoritativeLibraryEnabled {
		t.Error("expected AuthoritativeLibraryEnabled to be true after enabling")
	}
	// The write-side configuration fields must remain identical.
	before.AuthoritativeLibraryEnabled = true
	if before != after {
		t.Errorf("enabling authoritative-library mode changed the Calibre config\n before %+v\n  after %+v", before, after)
	}
	if !after.Enabled || !after.LibraryImportEnabled || !after.SyncOnStartup || after.LibraryPath != tmp {
		t.Errorf("the existing Calibre configuration no longer round-trips: %+v", after)
	}
}

// TestCalibreAuthoritativeLibrary_SavesAndReadsBack exercises the key through
// the generic settings endpoints, which is the machinery the issue requires it
// to use rather than a handler of its own.
func TestCalibreAuthoritativeLibrary_SavesAndReadsBack(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if err := repo.Set(ctx, SettingCalibreLibraryPath, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	req := withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreAuthoritativeLibraryEnabled,
		strings.NewReader(`{"value":"true"}`)), SettingCalibreAuthoritativeLibraryEnabled)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := repo.Get(ctx, SettingCalibreAuthoritativeLibraryEnabled)
	if err != nil || stored == nil {
		t.Fatalf("setting did not persist: %v", err)
	}
	if stored.Value != "true" {
		t.Errorf("stored %q, want %q", stored.Value, "true")
	}
	if !isCalibreAuthoritativeLibraryEnabled(t, repo) {
		t.Error("stored value does not read back as enabled")
	}

	// And back off again: the switch has to be reversible without leaving
	// the instance in a state the validators refuse.
	req = withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreAuthoritativeLibraryEnabled,
		strings.NewReader(`{"value":"false"}`)), SettingCalibreAuthoritativeLibraryEnabled)
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("turning the mode off: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if isCalibreAuthoritativeLibraryEnabled(t, repo) {
		t.Error("mode still reads as on after being turned off")
	}
}

// TestCalibreAuthoritativeLibrary_RejectsBadValue keeps a typo from being
// stored as something truthy-looking that nothing interprets.
func TestCalibreAuthoritativeLibrary_RejectsBadValue(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreAuthoritativeLibraryEnabled,
		strings.NewReader(`{"value":"yes"}`)), SettingCalibreAuthoritativeLibraryEnabled)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := repo.Get(ctx, SettingCalibreAuthoritativeLibraryEnabled); got != nil {
		t.Errorf("bad value was stored anyway: %+v", got)
	}
}

// TestCalibreAuthoritativeLibrary_NeedsALibraryPath: the mode reads
// metadata.db out of the configured Calibre library, so enabling it without
// one would store a configuration that cannot do anything.
func TestCalibreAuthoritativeLibrary_NeedsALibraryPath(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	req := withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreAuthoritativeLibraryEnabled,
		strings.NewReader(`{"value":"true"}`)), SettingCalibreAuthoritativeLibraryEnabled)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without a library path, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), SettingCalibreLibraryPath) {
		t.Errorf("the error does not name the missing key: %s", rec.Body.String())
	}
	if got, _ := repo.Get(ctx, SettingCalibreAuthoritativeLibraryEnabled); got != nil {
		t.Errorf("mode was enabled anyway: %+v", got)
	}

	// Turning it off is always allowed, library path or not, so an operator
	// can never be locked into the mode.
	req = withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreAuthoritativeLibraryEnabled,
		strings.NewReader(`{"value":"false"}`)), SettingCalibreAuthoritativeLibraryEnabled)
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("turning the mode off without a library path: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCalibreAuthoritativeLibrary_BlocksClearingTheLibraryPath is the same
// rule from the other side. Clearing the path while the mode is on would leave
// it with nothing to read, which is the silent-misconfiguration shape the
// settings registry exists to prevent.
func TestCalibreAuthoritativeLibrary_BlocksClearingTheLibraryPath(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	tmp := t.TempDir()
	if err := repo.Set(ctx, SettingCalibreLibraryPath, tmp); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "true"); err != nil {
		t.Fatal(err)
	}

	req := withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreLibraryPath,
		strings.NewReader(`{"value":""}`)), SettingCalibreLibraryPath)
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ := repo.Get(ctx, SettingCalibreLibraryPath)
	if stored == nil || stored.Value != tmp {
		t.Errorf("library path was cleared anyway: %+v", stored)
	}

	// With the mode off the path clears as it always has — the rule must not
	// leak into installs that never opted in.
	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	req = withKey(httptest.NewRequest(http.MethodPut,
		"/api/v1/setting/"+SettingCalibreLibraryPath,
		strings.NewReader(`{"value":""}`)), SettingCalibreLibraryPath)
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing the path with the mode off: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCalibreAuthoritativeLibrary_BlocksDeletingTheLibraryPath proves that
// deleting calibre.library_path while calibre.authoritative_library_enabled=true
// is rejected (400), leaving the existing library path stored, and that deleting
// succeeds once authoritative mode is off.
func TestCalibreAuthoritativeLibrary_BlocksDeletingTheLibraryPath(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	tmp := t.TempDir()
	if err := repo.Set(ctx, SettingCalibreLibraryPath, tmp); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "true"); err != nil {
		t.Fatal(err)
	}

	req := withKey(httptest.NewRequest(http.MethodDelete,
		"/api/v1/setting/"+SettingCalibreLibraryPath, nil), SettingCalibreLibraryPath)
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on DELETE while authoritative mode is on, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := repo.Get(ctx, SettingCalibreLibraryPath)
	if err != nil || stored == nil || stored.Value != tmp {
		t.Fatalf("library path changed or cleared after rejected DELETE: stored=%+v, err=%v", stored, err)
	}

	// Turn mode off and confirm DELETE succeeds.
	if err := repo.Set(ctx, SettingCalibreAuthoritativeLibraryEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	req = withKey(httptest.NewRequest(http.MethodDelete,
		"/api/v1/setting/"+SettingCalibreLibraryPath, nil), SettingCalibreLibraryPath)
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deleting path with mode off: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err = repo.Get(ctx, SettingCalibreLibraryPath)
	if err != nil {
		t.Fatalf("get library path: %v", err)
	}
	if stored != nil && stored.Value != "" {
		t.Errorf("library path still present after DELETE: stored=%+v", stored)
	}
}

// TestCalibreAuthoritativeLibrary_Descriptor pins what the registry advertises,
// since the default it carries is what a client renders before anything is
// stored.
func TestCalibreAuthoritativeLibrary_Descriptor(t *testing.T) {
	d, ok := LookupSettingDescriptor(SettingCalibreAuthoritativeLibraryEnabled)
	if !ok {
		t.Fatalf("%s has no descriptor", SettingCalibreAuthoritativeLibraryEnabled)
	}
	if d.Type != SettingTypeBool {
		t.Errorf("type = %q, want %q", d.Type, SettingTypeBool)
	}
	if d.Default != "false" {
		t.Errorf("default = %q, want false: the mode is opt-in", d.Default)
	}
	if d.State != SettingStateActive {
		t.Errorf("state = %q, want %q", d.State, SettingStateActive)
	}
	if d.Secret || d.AdminOnly {
		t.Errorf("the flag is not sensitive: secret=%v adminOnly=%v", d.Secret, d.AdminOnly)
	}
	if !d.Writable {
		t.Error("the settings UI writes this key, so it must be writable")
	}
}
